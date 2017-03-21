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
package proto

import (
	"compress/gzip"
	"errors"
	"io"
	"net"
	"strconv"

	"gopkg.in/vmihailenco/msgpack.v2"

	log "github.com/sirupsen/logrus"
	"github.com/zif/zif/common"
	"github.com/zif/zif/data"
	"github.com/zif/zif/dht"
)

const (
	EntryLengthMax = 1024
	MaxPageSize    = 25
)

type Client struct {
	conn net.Conn

	limiter *io.LimitedReader
	decoder *msgpack.Decoder
	encoder *msgpack.Encoder
}

// Creates a new client, automatically setting up the json encoder/decoder.
func NewClient(conn net.Conn) (*Client, error) {
	c := &Client{conn: conn}

	c.limiter = &io.LimitedReader{R: c.conn, N: common.MaxMessageSize}
	c.decoder = msgpack.NewDecoder(c.limiter)
	c.encoder = msgpack.NewEncoder(c.conn)

	return c, nil
}

func (c *Client) Terminate() {
	//c.conn.Write(proto_terminate)
}

// Close the client connection.
func (c *Client) Close() (err error) {
	if c.conn != nil {
		err = c.conn.Close()
	}
	return
}

// Encodes v as json and writes it to c.conn.
func (c *Client) WriteMessage(v interface{}) error {
	if c == nil {
		return errors.New("Client nil")
	}

	if c.encoder == nil {
		c.encoder = msgpack.NewEncoder(c.conn)
	}

	err := c.encoder.Encode(v)

	return err
}

func (c *Client) WriteErr(toSend error) error {
	msg := &Message{Header: ProtoNo}
	err := msg.Write(toSend.Error())

	if err != nil {
		return err
	}

	return c.WriteMessage(msg)
}

// Blocks until a message is read from c.conn, decodes it into a *Message and
// returns.
func (c *Client) ReadMessage() (*Message, error) {
	var msg Message

	if c.limiter == nil {
		c.limiter = &io.LimitedReader{R: c.conn, N: common.MaxMessageSize}
	}

	if c.decoder == nil {
		c.decoder = msgpack.NewDecoder(c.limiter)
	}

	if err := c.decoder.Decode(&msg); err != nil {
		c.limiter.N = common.MaxMessageSize
		return nil, err
	}

	msg.Stream = c.conn

	c.limiter.N = common.MaxMessageSize
	return &msg, nil
}

func (c *Client) Decode(i interface{}) error {
	return c.decoder.Decode(i)
}

// Sends a DHT entry to a peer.
func (c *Client) SendStruct(e common.Encoder) error {
	msg := Message{Header: ProtoDhtEntry}
	err := msg.Write(e)

	if err != nil {
		c.conn.Close()
		return err
	}

	c.WriteMessage(msg)

	return nil
}

// Announce the given DHT entry to a peer, passes on this peers details,
// meaning that it can be reached by other peers on the network.
func (c *Client) Announce(e common.Encoder) error {
	msg := &Message{
		Header: ProtoDhtAnnounce,
	}

	err := msg.Write(e)

	if err != nil {
		return err
	}

	err = c.WriteMessage(msg)

	if err != nil {
		return err
	}

	ok, err := c.ReadMessage()

	if err != nil {
		return err
	}

	if !ok.Ok() {
		return errors.New("Peer did not respond with ok")
	}

	return nil
}

func (c *Client) FindClosest(address dht.Address) ([]*dht.Entry, error) {
	// TODO: LimitReader

	msg := &Message{
		Header: ProtoDhtFindClosest,
	}

	err := msg.Write(address)

	if err != nil {
		return nil, err
	}

	// Tell the peer the address we are looking for
	err = c.WriteMessage(msg)

	if err != nil {
		return nil, err
	}

	log.Debug("Send FindClosest request")

	closest, err := c.ReadMessage()

	if err != nil {
		return nil, err
	}

	ret := make([]*dht.Entry, 0, 1)
	err = closest.Read(&ret)

	if err != nil {
		return nil, err
	}

	log.WithField("entries", len(ret)).Info("Find closest complete")

	return ret, err
}

func (c *Client) Query(address dht.Address) (*dht.Entry, error) {
	// TODO: LimitReader

	msg := &Message{
		Header: ProtoDhtQuery,
	}

	err := msg.Write(address)

	if err != nil {
		return nil, err
	}

	// Tell the peer the address we are looking for
	err = c.WriteMessage(msg)

	if err != nil {
		return nil, err
	}
	log.Debug("Written address")

	var entry dht.Entry
	er, err := c.ReadMessage()

	if err != nil {
		return nil, err
	}

	if er.Header == ProtoNo {
		return nil, errors.New("Peer returned no")
	}
	log.Debug("Recieved entry")

	err = er.Read(&entry)

	if err != nil {
		return nil, err
	}

	log.WithField("peer", entry.Address.StringOr("")).Debug("Decoded entry")

	err = entry.Verify()

	if err != nil {
		return nil, err
	}

	log.Debug("Verified entry")

	return &entry, nil
}

// Adds the initial entries into the given routing table. Essentially queries for
// both it's own and the peers address, storing the result. This means that after
// a bootstrap, it should be possible to connect to *any* peer!
func (c *Client) Bootstrap(d *dht.DHT, address dht.Address) error {
	defer c.Close()
	peers, err := c.FindClosest(address)

	if err != nil {
		return err
	}

	// add them all to our routing table! :D
	for _, i := range peers {
		if i.Address.Equals(&address) {
			continue
		}

		if i == nil {
			continue
		}

		err := i.Verify()

		if err != nil {
			log.WithField("address", i.Address.StringOr("")).Error("Bad peer, entry not valid: ", err.Error())
			continue
		}

		_, err = d.Insert(*i)

		if err != nil {
			return err
		}
	}

	if len(peers) > 1 {
		log.Info("Bootstrapped with ", len(peers), " new peers")
	} else if len(peers) == 1 {
		log.Info("Bootstrapped with 1 new peer")
	}

	return nil
}

// TODO: Paginate searches
func (c *Client) Search(search string, page int) ([]*data.Post, error) {
	log.WithField("Query", search).Info("Querying")

	sq := MessageSearchQuery{search, page}

	msg := &Message{
		Header: ProtoSearch,
	}

	err := msg.Write(sq)

	if err != nil {
		return nil, err
	}

	c.WriteMessage(msg)

	var posts []*data.Post

	recv, err := c.ReadMessage()

	if err != nil {
		return nil, err
	}

	err = recv.Read(&posts)

	if err != nil {
		return nil, err
	}

	return posts, nil
}

func (c *Client) Recent(page int) ([]*data.Post, error) {
	log.Info("Fetching recent posts from peer")

	msg := &Message{
		Header: ProtoRecent,
	}

	err := msg.Write(page)

	if err != nil {
		return nil, err
	}

	err = c.WriteMessage(msg)

	if err != nil {
		return nil, err
	}

	posts_msg, err := c.ReadMessage()

	if err != nil {
		return nil, err
	}

	var posts []*data.Post
	err = posts_msg.Read(&posts)

	if err != nil {
		return nil, err
	}

	log.Info("Recieved ", len(posts), " recent posts")

	return posts, nil
}

func (c *Client) Popular(page int) ([]*data.Post, error) {
	log.Info("Fetching popular posts from peer")

	msg := &Message{
		Header: ProtoPopular,
	}

	err := msg.Write(page)

	if err != nil {
		return nil, err
	}

	err = c.WriteMessage(msg)

	if err != nil {
		return nil, err
	}

	posts_msg, err := c.ReadMessage()

	if err != nil {
		return nil, err
	}

	var posts []*data.Post
	err = posts_msg.Read(&posts)

	if err != nil {
		return nil, err
	}

	log.Info("Recieved ", len(posts), " popular posts")

	return posts, nil
}

// Download a hash list for a peer. Expects said hash list to be valid and
// signed.
func (c *Client) Collection(address dht.Address, entry dht.Entry) (*MessageCollection, error) {
	log.WithField("for", address.StringOr("")).Info("Sending request for a collection")

	msg := &Message{
		Header: ProtoRequestHashList,
	}

	err := msg.Write(address)

	if err != nil {
		return nil, err
	}

	err = c.WriteMessage(msg)

	if err != nil {
		return nil, err
	}

	log.Debug("Sent hash list request")

	hl, err := c.ReadMessage()

	if err != nil {
		return nil, err
	}

	log.Debug("Recieved response")

	mhl := MessageCollection{}
	err = hl.Read(&mhl)

	if err != nil {
		return nil, err
	}

	log.Debug("Read hash list ok")

	err = mhl.Verify(entry.CollectionHash)

	if err != nil {
		return nil, err
	}

	log.WithField("pieces", mhl.Size).Info("Recieved valid collection")

	return &mhl, nil
}

// Download a piece from a peer, given the address and id of the piece we want.
func (c *Client) Pieces(address dht.Address, id, length int) chan *data.Piece {
	log.WithFields(log.Fields{
		"address": address.StringOr(""),
		"id":      id,
		"length":  length,
	}).Info("Sending request for piece")

	ret := make(chan *data.Piece, 100)

	mrp := MessageRequestPiece{address.StringOr(""), id, length}

	msg := &Message{
		Header: ProtoRequestPiece,
	}

	err := msg.Write(mrp)

	if err != nil {
		log.Error(err.Error())
		return nil
	}

	err = c.WriteMessage(msg)

	if err != nil {
		log.Error(err.Error())
		return nil
	}

	// Convert a string to an int, prevents endless error checks below.
	convert := func(val string) int {
		ret, err := strconv.Atoi(val)

		if err != nil {
			log.Error(err.Error())
			return 0
		}

		return ret
	}

	go func() {
		defer close(ret)
		log.Info("Recieving pieces")

		gzr, err := gzip.NewReader(c.conn)

		if err != nil {
			log.Error(err.Error())
			return
		}

		errReader := data.NewErrorReader(gzr)

		for i := 0; i < length; i++ {
			piece := data.Piece{}
			piece.Setup()

			count := 0
			for {
				if count >= data.PieceSize {
					break
				}

				id_s := errReader.ReadString('|')
				id := convert(id_s)

				if id == -1 {
					break
				}

				ih := errReader.ReadString('|')
				title := errReader.ReadString('|')
				size := convert(errReader.ReadString('|'))
				filecount := convert(errReader.ReadString('|'))
				seeders := convert(errReader.ReadString('|'))
				leechers := convert(errReader.ReadString('|'))
				uploaddate := convert(errReader.ReadString('|'))
				tags := errReader.ReadString('|')
				meta := errReader.ReadString('|')

				if errReader.Err != nil {
					log.Error("Failed to read post: ", errReader.Err.Error())
					break
				}

				if err != nil {
					log.Error(err.Error())
				}

				post := data.Post{
					Id:         id,
					InfoHash:   ih,
					Title:      title,
					Size:       size,
					FileCount:  filecount,
					Seeders:    seeders,
					Leechers:   leechers,
					UploadDate: uploaddate,
					Tags:       tags,
					Meta:       meta,
				}

				piece.Add(post, true)
				count++
			}
			ret <- &piece
		}
	}()

	return ret
}

func (c *Client) RequestAddPeer(addr dht.Address) error {
	log.WithField("for", addr.StringOr("")).Info("Registering as seed")

	msg := &Message{
		Header: ProtoRequestAddPeer,
	}

	err := msg.Write(addr)

	if err != nil {
		return err
	}

	err = c.WriteMessage(msg)

	if err != nil {
		return err
	}

	rep, err := c.ReadMessage()

	if err != nil {
		return err
	}

	if !rep.Ok() {
		return errors.New("Peer add request failed")
	}

	log.Info("Registered as seed peer")

	return nil
}
