package proto

import (
	"net"

	msgpack "gopkg.in/vmihailenco/msgpack.v2"

	"github.com/zif/zif/dht"
)

type Message struct {
	Header  string
	Content []byte

	Stream net.Conn
	Client *Client
	From   *dht.Address
}

func (m *Message) WriteInt(i int) {
	j, _ := msgpack.Marshal(i)
	m.Content = make([]byte, len(j))

	copy(m.Content, j)
}

func (m *Message) ReadInt() (int, error) {
	var ret int
	err := msgpack.Unmarshal(m.Content, &ret)

	return ret, err
}

func (m *Message) Json() ([]byte, error) {
	return msgpack.Marshal(m)
}

func (m *Message) Decode(iface interface{}) error {
	err := msgpack.Unmarshal(m.Content, iface)

	return err
}

// Ok() is just an easier way to check if the peer has sent an "ok" response,
// rather than comparing the header member to a constant repeatedly.
func (m *Message) Ok() bool {
	return m.Header == ProtoOk
}
