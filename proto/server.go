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

// tcp server

import (
	"encoding/binary"
	"io"
	"net"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/zif/zif/common"
	"github.com/zif/zif/util"
)

type Server struct {
	listener     net.Listener
	capabilities *MessageCapabilities
}

func NewServer(cap *MessageCapabilities) *Server {
	ret := &Server{}

	ret.capabilities = cap

	return ret
}

func (s *Server) Listen(addr string, handler ProtocolHandler, data common.Encoder) {
	var err error

	s.listener, err = net.Listen("tcp", addr)

	if err != nil {
		panic(err)
	}

	log.WithField("address", addr).Info("Listening")

	for {
		conn, err := s.listener.Accept()

		if err != nil {
			log.Error(err.Error())
			continue
		}

		log.Info("New TCP connection")

		var zif int16
		binary.Read(conn, binary.LittleEndian, &zif)

		if zif != ProtoZif {
			log.Error("This is not a Zif connection: ", zif)
			continue
		}

		log.Debug("Zif connection")

		var version int16
		binary.Read(conn, binary.LittleEndian, &version)

		if version != ProtoVersion {
			log.Error("Incorrect protocol version: ", version)
			continue
		}

		log.Debug("Correct version")

		log.Debug("Handshaking new connection")
		go s.Handshake(conn, handler, data)
	}
}

func (s *Server) ListenStream(peer NetworkPeer, handler ProtocolHandler) {
	// Allowed to open 4 streams per second, bursting to three.
	limiter := util.NewLimiter(time.Second/4, 3, true)
	defer limiter.Stop()
	defer handler.HandleCloseConnection(peer.Address())

	session := peer.Session()

	for {
		stream, err := session.Accept()
		limiter.Wait()

		if err != nil {
			if err == io.EOF {
				log.Info("Peer closed connection")

			} else {
				log.Error(err.Error())
			}

			return
		}

		err = stream.SetDeadline(time.Now().Add(time.Second * 10))

		if err != nil {
			log.Error(err.Error())
			return
		}

		log.Debug("Accepted stream (", session.NumStreams(), " total)")

		peer.AddStream(stream)
		peer.UpdateSeen()

		go s.HandleStream(peer, handler, stream)
	}
}

func (s *Server) HandleStream(peer NetworkPeer, handler ProtocolHandler, stream net.Conn) {
	log.Debug("Handling stream")

	cl, err := NewClient(stream)

	if err != nil {
		log.Error(err.Error())
		return
	}

	for {
		msg, err := cl.ReadMessage()

		if err != nil {
			if err != io.EOF {
				log.Error(err.Error())
			}
			return
		}
		msg.Client = cl
		msg.From = peer.Address()

		s.RouteMessage(msg, handler)
	}
}

func (s *Server) RouteMessage(msg *Message, handler ProtocolHandler) {
	var err error

	defer msg.Client.Close()

	switch msg.Header {

	case ProtoDhtAnnounce:
		err = handler.HandleAnnounce(msg)
	case ProtoDhtQuery:
		err = handler.HandleQuery(msg)
	case ProtoDhtFindClosest:
		err = handler.HandleFindClosest(msg)
	case ProtoSearch:
		err = handler.HandleSearch(msg)
	case ProtoRecent:
		err = handler.HandleRecent(msg)
	case ProtoPopular:
		err = handler.HandlePopular(msg)
	case ProtoRequestHashList:
		err = handler.HandleHashList(msg)
	case ProtoRequestPiece:
		err = handler.HandlePiece(msg)
	case ProtoRequestAddPeer:
		err = handler.HandleAddPeer(msg)

	default:
		log.Error("Unknown message type")

	}

	if err != nil {
		log.Error(err.Error())
	}

}

func (s *Server) Handshake(conn net.Conn, lp ProtocolHandler, data common.Encoder) {
	cl, err := NewClient(conn)

	if err != nil {
		log.Error(err.Error())
		return
	}

	header, caps, err := handshake(*cl, lp, data)

	if err != nil {
		log.Error(err.Error())
		return
	}

	peer, err := lp.HandleHandshake(ConnHeader{*cl, *header, *caps})

	if err != nil {
		log.Error(err.Error())
		return
	}

	lp.SetCapabilities(*caps)
	lp.SetNetworkPeer(peer)

	go s.ListenStream(peer, lp)
}

func (s *Server) Close() {
	if s.listener != nil {
		s.listener.Close()
	}
}
