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
package zif

import (
	"database/sql"
	"errors"
	"fmt"
	"io/ioutil"
	"strconv"
	"time"

	"github.com/spf13/viper"
	"github.com/streamrail/concurrent-map"
	"github.com/zif/zif/data"
	"github.com/zif/zif/dht"

	log "github.com/sirupsen/logrus"
)

const HeartbeatFrequency = time.Second * 30
const AnnounceFrequency = time.Minute * 30

// errors

var (
	PeerUnreachable  = errors.New("Peer could not be reached")
	PeerDisconnected = errors.New("Peer has disconnected")
	RecursionLimit   = errors.New("Recursion limit reached, peer cannot be resolved")
)

// handles peer connections
type PeerManager struct {
	// a map of currently connected peers
	// also use to cancel reconnects :)
	peers cmap.ConcurrentMap
	// maps a peer address to when it was last seen
	peerSeen cmap.ConcurrentMap
	// A map of public address to Zif address
	publicToZif  cmap.ConcurrentMap
	seedManagers cmap.ConcurrentMap

	socks     bool
	socksPort int
	localPeer *LocalPeer
}

func NewPeerManager(lp *LocalPeer) *PeerManager {
	ret := &PeerManager{}

	ret.peers = cmap.New()
	ret.publicToZif = cmap.New()
	ret.seedManagers = cmap.New()
	ret.peerSeen = cmap.New()
	ret.localPeer = lp

	return ret
}

func (pm *PeerManager) Count() int {
	return pm.peers.Count()
}

func (pm *PeerManager) Peers() map[string]*Peer {
	ret := make(map[string]*Peer)

	for k, v := range pm.peers.Items() {
		ret[k] = v.(*Peer)
	}

	return ret
}

// Given a direct address, for instance an IP or domain, connect to the peer there.
// This can be used for something like bootstrapping, or for something like
// connecting to a peer whose Zif address we have just resolved.
func (pm *PeerManager) ConnectPeerDirect(addr string) (*Peer, error) {
	var peer *Peer
	var err error

	zifAddr, ok := pm.publicToZif.Get(addr)
	if ok {
		if peer = pm.GetPeer(*zifAddr.(*dht.Address)); peer != nil {
			return peer, nil
		}
	}

	peer = &Peer{}

	if pm.socks {
		peer.streams.Socks = true
		peer.streams.SocksPort = pm.socksPort
	}

	err = peer.Connect(addr, pm.localPeer)

	if err != nil {
		return nil, PeerUnreachable
	}

	peer.ConnectClient(pm.localPeer)

	pm.SetPeer(peer)

	return peer, nil
}

// Resolved a Zif address into an entry, connects to the peer at the
// PublicAddress in the Entry, then return it. The peer is also stored in a map.
func (pm *PeerManager) ConnectPeer(addr dht.Address) (*Peer, *dht.Entry, error) {
	var peer *Peer

	entry, err := pm.Resolve(addr)

	if err != nil {
		return nil, nil, err
	}

	if entry.Address.Equals(pm.localPeer.Address()) {
		return nil, nil, errors.New("Cannot connect to self")
	}

	if entry == nil {
		return nil, nil, data.AddressResolutionError{Address: entry.Address.StringOr("")}
	}

	if peer = pm.GetPeer(entry.Address); peer != nil {
		return peer, entry, nil
	}

	if err != nil {
		log.Error(err.Error())
		return nil, entry, err
	}

	// now should have an entry for the peer, connect to it!
	log.WithField("address", entry.Address.StringOr("")).Debug("Connecting")

	peer, err = pm.ConnectPeerDirect(entry.PublicAddress + ":" + strconv.Itoa(entry.Port))

	// Caller can go on to choose a seed to connect to, not quite the end of the
	// world :P
	if err != nil {
		log.WithField("peer", addr.StringOr("")).Info("Failed to connect")

		return nil, entry, err
	}

	return peer, entry, nil
}

func (pm *PeerManager) GetPeer(addr dht.Address) *Peer {
	peer, has := pm.peers.Get(string(addr.Raw))

	if !has {
		return nil
	}

	return peer.(*Peer)
}

func (pm *PeerManager) SetPeer(p *Peer) {

	if pm.peers.Has(string(p.Address().Raw)) {
		return
	}

	e, err := p.Entry()

	if err != nil {
		log.Error(err.Error())
		return
	}

	pm.publicToZif.Set(e.PublicAddress, p.Address())

	p.addSeedManager = pm.AddSeedManager
	p.addEntry = pm.localPeer.AddEntry
	p.addSeeding = pm.localPeer.AddSeeding

	p.updateSeen = func() {
		pm.peerSeen.Set(string(p.Address().Raw), time.Now().UnixNano())
	}

	pm.peers.Set(string(p.Address().Raw), p)
	pm.peerSeen.Set(string(p.Address().Raw), time.Now().UnixNano())

	// if we need to clear space for another, remove the least recently used one
	for pm.peers.Count() > viper.GetInt("net.maxPeers") {

		oldestKey := ""
		oldestValue := int64(time.Now().UnixNano())

		// find the least recently seen peer
		for i := range pm.peerSeen.IterBuffered() {
			if i.Val == nil {
				continue
			}

			time := i.Val.(int64)

			if time < oldestValue {
				oldestKey = i.Key
			}
		}

		// then remove it, after disconnecting it from the network
		peer, ok := pm.peers.Get(oldestKey)

		if !ok {
			log.Error("Could not get peer for removal")
			break
		}

		switch peer.(type) {
		case *Peer:
			log.WithField("removing", peer.(*Peer).Address().StringOr("")).Info("Too many peers connected")
			peer.(*Peer).Terminate()
			pm.HandleCloseConnection(peer.(*Peer).Address())
		default:
			log.Error("Value was not *Peer")
		}

	}

	go pm.heartbeatPeer(p)
	go pm.announcePeer(p)
}

func (pm *PeerManager) HandleCloseConnection(addr *dht.Address) {
	pm.peers.Remove(string(addr.Raw))
	pm.peerSeen.Remove(string(addr.Raw))

	sm, ok := pm.seedManagers.Get(string(addr.Raw))

	if ok {
		sm.(*SeedManager).Close <- true
	}
}

// Pings the peer regularly to check the connection
func (pm *PeerManager) heartbeatPeer(p *Peer) {
	ticker := time.NewTicker(HeartbeatFrequency)
	defer pm.HandleCloseConnection(p.Address())

	for _ = range ticker.C {
		// just in case
		if p == nil {
			return
		}

		// If the peer has already been removed, don't bother
		if has := pm.peers.Has(string(p.Address().Raw)); !has {
			return
		}

		log.WithField("peer", p.Address().StringOr("")).Debug("Sending heartbeat")
		// allows for a suddenly slower connection, most requests have a lower timeout
		_, err := p.Ping(HeartbeatFrequency)

		if err != nil {
			log.WithField("peer", p.Address().StringOr("")).Info("Peer has no heartbeat, terminating")

			pm.HandleCloseConnection(p.Address())

			return
		}
	}
}

func (pm *PeerManager) announcePeer(p *Peer) {
	ticker := time.NewTicker(AnnounceFrequency)

	announce := func() error {
		// just in case
		if p == nil {
			return errors.New("Peer is nil")
		}

		// If the peer has already been removed, don't bother
		if has := pm.peers.Has(string(p.Address().Raw)); !has {
			return PeerDisconnected
		}

		log.WithField("peer", p.Address().StringOr("")).Info("Announcing to peer")
		err := p.Announce(pm.localPeer)

		if err != nil {
			return err
		}

		return nil
	}

	err := announce()

	if err != nil {
		log.Error(err.Error())
	}

	for _ = range ticker.C {
		err := announce()

		if err != nil {
			log.Error(err.Error())
		}
	}
}

func (pm *PeerManager) AddSeedManager(addr dht.Address) error {
	if pm.seedManagers.Has(string(addr.Raw)) {
		return nil
	}

	log.WithField("peer", addr.StringOr("")).Info("Loading seed manager")

	sm, err := NewSeedManager(addr, pm.localPeer)

	if err != nil {
		return err
	}

	pm.seedManagers.Set(string(addr.Raw), sm)
	sm.Start()

	return nil
}

func (pm *PeerManager) LoadSeeds() error {
	log.Info("Loading seed list")
	file, err := ioutil.ReadFile("./data/seeding.dat")

	if err != nil {
		return err
	}

	seedCount := len(file) / 20

	for i := 0; i < seedCount; i++ {
		addr := dht.Address{Raw: file[i*20 : 20+i*20]}

		err := pm.AddSeedManager(addr)

		if err != nil {
			log.Error(err.Error())
		}
	}
	log.Info("Finished loading seed list")

	return err
}

// Resolves a Zif address into an entry. Hopefully we already have the entry,
// in which case it's just loaded from disk. Otherwise, recursive network
// queries are made to try and find it.
func (pm *PeerManager) Resolve(addr dht.Address) (*dht.Entry, error) {
	log.WithField("address", addr.StringOr("")).Debug("Resolving")

	if addr.Equals(pm.localPeer.Address()) {
		return pm.localPeer.Entry, nil
	}

	kv, err := pm.localPeer.DHT.Query(addr)

	if err == sql.ErrNoRows {
		err = nil
	}

	if err != nil {
		return nil, err
	}

	if kv != nil {
		return kv, nil
	}

	// gets an initial set to work with
	closest, err := pm.localPeer.DHT.FindClosest(addr)

	if err != nil {
		return nil, err
	}

	depth := 6
	for _, i := range closest {
		// TODO: Goroutine this.
		entry, err := pm.resolveStep(i, addr, &depth)

		if err != nil {
			if err == RecursionLimit {
				return nil, err
			}

			log.Error(err.Error())
			continue
		}

		if entry == nil {
			continue
		}

		if entry.Address.Equals(&addr) {
			pm.localPeer.DHT.Insert(*entry)

			return entry, nil
		}
	}

	return nil, errors.New("Address could not be resolved")
}

// Will return the entry itself, or an error.
func (pm *PeerManager) resolveStep(e *dht.Entry, addr dht.Address, depth *int) (*dht.Entry, error) {
	// connect to the peer
	var peer *Peer
	var err error

	if *depth == 0 {
		return nil, RecursionLimit
	}
	*depth -= 1

	log.WithField("peer", e.Address.StringOr("")).Info("Querying for resolve")

	peer = pm.GetPeer(e.Address)

	if peer == nil {
		peer, err = pm.ConnectPeerDirect(fmt.Sprintf("%s:%d", e.PublicAddress, e.Port))

		if err != nil {
			return nil, err
		}
	}

	kv, err := peer.Query(addr)

	if err != nil {
		return nil, err
	}

	if kv != nil {
		entry := kv
		return entry.(*dht.Entry), err
	}

	closest, err := peer.FindClosest(addr)

	if err != nil {
		return nil, err
	}

	for _, i := range closest {
		entry := i.(*dht.Entry)

		result, err := pm.resolveStep(entry, addr, depth)

		if err != nil {
			return nil, err
		}

		if result != nil {
			return result, nil
		}
	}

	return nil, errors.New("No entries could be found")
}
