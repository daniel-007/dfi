/*
 *  Zif
 *  Copyright (C) 2017 Zif LTD
 *
 *  This program is free software: you can redistribute it and/or modify
 *  it under the terms of the GNU Affero General Public License as published
 *  by the Free Software Foundation, either version 3 of the License, or
 *  (at your option) any later version.
 *
 *  This program is distributed in the hope that it will be useful,
 *  but WITHOUT ANY WARRANTY; without even the implied warranty of
 *  MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 *  GNU Affero General Public License for more details.

 *  You should have received a copy of the GNU Affero General Public License
 *  along with this program.  If not, see <http://www.gnu.org/licenses/>.
 */
// This represents a peer in the network.
// the minimum that a peer requires to be "valid" is just an address.
// everything else can be discovered via the network.
// Just a bit of a wrapper for the client really, that contains most of the networking code, this mostly has the data and a few other things.

package zif

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"net"
	"time"

	"github.com/hashicorp/yamux"
	log "github.com/sirupsen/logrus"
	"golang.org/x/crypto/ed25519"

	"github.com/zif/zif/data"
	"github.com/zif/zif/dht"
	"github.com/zif/zif/proto"
	"github.com/zif/zif/util"

	"github.com/zif/zif/common"
)

type Peer struct {
	address dht.Address

	publicKey ed25519.PublicKey
	streams   proto.StreamManager

	limiter *util.PeerLimiter

	entry *dht.Entry

	// If this peer is acting as a seed for another
	seed    bool
	seedFor *dht.Entry

	capabilities proto.MessageCapabilities
	compression  string

	addSeedManager func(dht.Address) error
	addSeeding     func(dht.Entry) error
	addEntry       func(dht.Entry) error
	updateSeen     func()
}

func (p *Peer) UpdateSeen() {
	if p.updateSeen != nil {
		p.updateSeen()
	}
}

func (p *Peer) EAddress() common.Encoder {
	return &p.address
}

func (p *Peer) Address() *dht.Address {
	return &p.address
}

func (p *Peer) PublicKey() []byte {
	return p.publicKey
}

func (p *Peer) Streams() *proto.StreamManager {
	return &p.streams
}

func (p *Peer) Ping(timeOut time.Duration) (time.Duration, error) {
	type timeErr struct {
		t   time.Duration
		err error
	}

	session := p.streams.GetSession()

	if session == nil {
		return -1, errors.New("No session")
	}

	if session.IsClosed() {
		return -1, errors.New("Session closed")
	}

	timer := time.NewTimer(timeOut)

	ret := make(chan timeErr)

	go func() {
		t, err := session.Ping()
		ret <- timeErr{t, err}
	}()

	select {
	case ping := <-ret:
		return ping.t, ping.err

	case _ = <-timer.C:
		return -1, errors.New("Timeout")
	}
}

func (p *Peer) Announce(lp *LocalPeer) error {
	_, err := p.Ping(time.Second * 10)
	if err != nil {
		return err
	}

	log.WithField("peer", p.Address().StringOr("")).Debug("Sending announce")

	if lp.Entry.PublicAddress == "" {
		log.Debug("Local peer public address is nil, attempting to fetch")
		ip := ExternalIp()
		log.Debug("External IP is ", ip)
		lp.Entry.PublicAddress = ip
	}
	lp.SignEntry()

	stream, err := p.OpenStream()

	if err != nil {
		return err
	}

	defer stream.Close()

	err = stream.Announce(lp.Entry)

	return err
}

func (p *Peer) Connect(addr string, lp *LocalPeer) error {
	log.WithField("address", addr).Debug("Connecting")

	pair, err := p.streams.OpenTCP(addr, lp, lp.Entry)

	if err != nil {
		return err
	}

	p.SetCapabilities(pair.Capabilities)
	p.publicKey = pair.Entry.PublicKey
	p.address = pair.Entry.Address

	p.limiter = &util.PeerLimiter{}
	p.limiter.Setup()

	lp.DHT.Insert(pair.Entry)

	return nil
}

func (p *Peer) SetTCP(header proto.ConnHeader) {
	p.streams.SetConnection(header)

	p.publicKey = header.Entry.PublicKey
	p.address = header.Entry.Address

	p.limiter = &util.PeerLimiter{}
	p.limiter.Setup()
}

func (p *Peer) ConnectServer() (*yamux.Session, error) {
	return p.streams.ConnectServer()
}

func (p *Peer) ConnectClient(lp *LocalPeer) (*yamux.Session, error) {
	client, err := p.streams.ConnectClient()

	if err != nil {
		return client, err
	}

	go lp.ListenStream(p)

	return client, err
}

func (p *Peer) Session() *yamux.Session {
	return p.streams.GetSession()
}

func (p *Peer) Terminate() {
	p.streams.Close()
}

func (p *Peer) OpenStream() (*proto.Client, error) {
	_, err := p.Ping(time.Second * 10)
	if err != nil {
		return nil, err
	}

	p.UpdateSeen()

	s, err := p.streams.OpenStream()
	return s, err
}

func (p *Peer) AddStream(conn net.Conn) {
	p.streams.AddStream(conn)
}

func (p *Peer) RemoveStream(conn net.Conn) {
	p.streams.RemoveStream(conn)
}

func (p *Peer) GetStream(conn net.Conn) *proto.Client {
	return p.streams.GetStream(conn)
}

func (p *Peer) CloseStreams() {
	p.streams.Close()
}

func (p *Peer) Entry() (*dht.Entry, error) {
	if p.entry != nil {
		return p.entry, nil
	}

	return p.GetEntry()
}

func (p *Peer) GetEntry() (*dht.Entry, error) {
	_, err := p.Ping(time.Second * 10)
	if err != nil {
		return nil, err
	}

	e, err := p.Query(*p.Address())

	if err != nil {
		return nil, err
	}

	entry := e.(*dht.Entry)

	log.WithField("for", p.Address().StringOr("")).Info("Recieved entry")

	if !entry.Address.Equals(p.Address()) {
		return nil, errors.New("Failed to fetch entry")
	}

	p.entry = entry

	return p.entry, nil
}

func (p *Peer) Bootstrap(d *dht.DHT) error {
	_, err := p.Ping(time.Second * 10)
	if err != nil {
		return err
	}

	stream, err := p.OpenStream()

	if err != nil {
		return err
	}

	defer stream.Close()

	return stream.Bootstrap(d, d.Address())
}

func (p *Peer) Query(address dht.Address) (common.Verifier, error) {
	_, err := p.Ping(time.Second * 10)
	if err != nil {
		return nil, err
	}

	addressString, _ := address.String()
	log.WithField("target", addressString).Info("Querying")

	stream, err := p.OpenStream()

	if err != nil {
		return nil, err
	}

	defer stream.Close()

	entry, err := stream.Query(address)

	return entry, err
}

func (p *Peer) FindClosest(address dht.Address) ([]common.Verifier, error) {
	_, err := p.Ping(time.Second * 10)
	if err != nil {
		return nil, err
	}

	addressString, _ := address.String()
	log.WithField("target", addressString).Info("Finding closest")

	stream, err := p.OpenStream()

	if err != nil {
		return nil, err
	}

	defer stream.Close()

	res, err := stream.FindClosest(address)

	ret := make([]common.Verifier, 0, len(res))

	for _, i := range res {
		ret = append(ret, i)
	}

	return ret, err
}

// asks a peer to query its database and return the results
func (p *Peer) Search(search string, page int) (*data.SearchResult, error) {
	_, err := p.Ping(time.Second * 10)
	if err != nil {
		return nil, err
	}

	log.WithField("peer", p.Address().StringOr("")).Info("Searching")
	stream, err := p.OpenStream()

	if err != nil {
		return nil, err
	}

	defer stream.Close()

	posts, err := stream.Search(search, page)
	res := &data.SearchResult{
		Posts:  posts,
		Source: p.Address().StringOr(""),
	}

	if err != nil {
		return nil, err
	}

	return res, nil
}

func (p *Peer) Recent(page int) ([]*data.Post, error) {
	_, err := p.Ping(time.Second * 10)
	if err != nil {
		return nil, err
	}

	stream, err := p.OpenStream()

	if err != nil {
		return nil, err
	}

	defer stream.Close()

	posts, err := stream.Recent(page)

	return posts, err

}

func (p *Peer) Popular(page int) ([]*data.Post, error) {
	_, err := p.Ping(time.Second * 10)
	if err != nil {
		return nil, err
	}

	stream, err := p.OpenStream()

	if err != nil {
		return nil, err
	}

	defer stream.Close()

	posts, err := stream.Popular(page)

	return posts, err

}

func (p *Peer) Mirror(db *data.Database, lp dht.Address, onPiece chan int) error {
	_, err := p.Ping(time.Second * 10)
	if err != nil {
		return err
	}

	pieces := make(chan *data.Piece, data.PieceSize)
	defer close(onPiece)

	go db.InsertPieces(pieces, true)

	var entry *dht.Entry
	if p.seed {
		e, err := p.Query(p.seedFor.Address)

		if err != nil {
			return err
		}

		entry = e.(*dht.Entry)
	} else {
		_, err = p.GetEntry()

		if err != nil {
			return err
		}

		entry, err = p.Entry()

		if err != nil {
			return err
		}
	}

	p.addEntry(*entry)

	log.WithField("peer", entry.Address.StringOr("")).Info("Mirroring")

	if err != nil {
		return err
	}
	stream, err := p.OpenStream()

	if err != nil {
		return err
	}

	defer stream.Close()

	mcol, err := stream.Collection(entry.Address, *entry)

	if err != nil {
		return err
	}

	collection := data.Collection{HashList: mcol.HashList}

	err = collection.Save(fmt.Sprintf("./data/%s/collection.dat", entry.Address.StringOr("err")))

	if err != nil {
		return err
	}

	if int(db.PostCount()) == entry.PostCount {
		return nil
	}

	currentStore := int(math.Ceil(float64(db.PostCount()) / float64(data.PieceSize)))

	since := 0
	if currentStore != 0 {
		since = currentStore - 1
	}

	log.WithField("size", mcol.Size).Info("Downloading collection")

	pieceStream, err := p.OpenStream()

	if err != nil {
		return err
	}

	defer pieceStream.Close()

	piece_chan := pieceStream.Pieces(entry.Address, since, mcol.Size)

	i := 0
	for piece := range piece_chan {
		log.Info(len(mcol.HashList))
		hash := piece.Hash()

		if !bytes.Equal(mcol.HashList[32*i:32*i+32], hash) {
			return errors.New("Piece hash mismatch")
		}

		onPiece <- i

		if len(pieces) == 100 {
			log.Info("Piece buffer full, io is blocking")
		}
		pieces <- piece

		i++
	}

	log.Info("Mirror complete, generating index")

	if err != nil {
		return err
	}

	err = p.RequestAddPeer(*entry)

	// we're done mirroring, so now we need to switch OFF the fact that this is
	// a seed. If it becomes a seed again, it will be properly set by the
	// commandserver
	p.seed = false
	p.seedFor = nil

	pieces <- nil

	return err
}

func (p *Peer) RequestAddPeer(entry dht.Entry) error {
	_, err := p.Ping(time.Second * 10)
	if err != nil {
		return err
	}

	stream, err := p.OpenStream()

	if err != nil {
		return err
	}

	defer stream.Close()

	err = stream.RequestAddPeer(entry.Address)
	if err != nil {
		return err
	}

	err = p.addSeedManager(entry.Address)

	if err != nil {
		return err
	}

	// first register the peer as a seed for the entry given
	for _, i := range entry.Seeds {
		seedAddr := &dht.Address{Raw: i}

		if seedAddr.Equals(&entry.Address) {
			return nil
		}
	}

	return p.addSeeding(entry)
}

func (p *Peer) GetCapabilities() *proto.MessageCapabilities {
	return &p.capabilities
}

func (p *Peer) SetCapabilities(caps proto.MessageCapabilities) {
	p.capabilities = caps
}

func (p *Peer) NewMessage(header string) *proto.Message {
	ret := &proto.Message{
		Header:      header,
		Compression: p.compression,
	}

	return ret
}
