package proxy

import (
	"net"
	"time"

	"sleepy/internal/protocol"
)

// clientConn reads packets from a client until the connection is handed over to the relay.
type clientConn struct {
	net.Conn
	reader *protocol.Reader
	buf    []byte
}

func newClientConn(c net.Conn) *clientConn {
	return &clientConn{Conn: c, reader: protocol.NewReader(protocol.DefaultMaxLength), buf: make([]byte, 4096)}
}

// fill reads one chunk into the packet reader. An error is returned only when no byte was read.
func (c *clientConn) fill() error {
	n, err := c.Conn.Read(c.buf)
	if n > 0 {
		c.reader.Push(c.buf[:n])
		return nil
	}
	return err
}

// next returns the next complete packet, reading as needed (bounded by the read deadline).
func (c *clientConn) next() (*protocol.RawPacket, error) {
	for {
		p, err := c.reader.Next()
		if err != nil || p != nil {
			return p, err
		}
		if err := c.fill(); err != nil {
			return nil, err
		}
	}
}

// watchClose keeps buffering what the client sends (to replay it later) while a player waits, and
// closes the returned channel if the client disconnects. stop() ends the watch and must be called
// before using the connection again.
func (c *clientConn) watchClose() (closed <-chan struct{}, stop func()) {
	closedCh := make(chan struct{})
	done := make(chan struct{})
	c.SetReadDeadline(time.Time{})
	go func() {
		defer close(done)
		for {
			if err := c.fill(); err != nil {
				if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
					close(closedCh)
				}
				return
			}
		}
	}()
	return closedCh, func() {
		c.SetReadDeadline(time.Now()) // unblocks the pending Read with a timeout
		<-done
		c.SetReadDeadline(time.Time{})
	}
}
