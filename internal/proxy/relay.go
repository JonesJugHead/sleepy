package proxy

import (
	"io"
	"net"
	"time"

	"sleepy/internal/backend"
	"sleepy/internal/logx"
	"sleepy/internal/protocol"
)

// closeGrace: once one side has finished, how long the other gets to finish too before both close.
const closeGrace = 5 * time.Second

// relay connects to the server, replays the bytes already read from the client (handshake,
// login start, anything buffered after), then copies both ways without looking at the data.
// player is set for player connections (counted for inactivity); failPacket is sent to the client
// if the server cannot be reached.
func relay(client net.Conn, replay []byte, b *backend.Backend, player string, failPacket []byte) {
	defer client.Close()
	upstream, err := net.DialTimeout("tcp", b.Address(), 5*time.Second)
	if err != nil {
		if player != "" {
			logx.Printf("Cannot reach the server for %s: %v", player, err)
		}
		if failPacket != nil {
			client.Write(failPacket)
		}
		return
	}
	defer upstream.Close()
	setNoDelay(upstream)

	if player != "" {
		logx.Printf("%s joined (%d connected)", player, b.Acquire())
		defer func() { logx.Printf("%s left (%d connected)", player, b.Release()) }()
	}
	if b.ProxyProtocol() {
		// Tells the server the player's real address; it must come before any Minecraft byte.
		replay = append(protocol.ProxyHeaderV2(client.RemoteAddr(), client.LocalAddr()), replay...)
	}
	if _, err := upstream.Write(replay); err != nil {
		return
	}

	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		io.Copy(dst, src)
		// Half-close so pending data (a kick message) is delivered before the connection ends.
		if tcp, ok := dst.(*net.TCPConn); ok {
			tcp.CloseWrite()
		}
		done <- struct{}{}
	}
	go pipe(upstream, client)
	go pipe(client, upstream)
	<-done
	select {
	case <-done:
	case <-time.After(closeGrace):
	}
}

func setNoDelay(c net.Conn) {
	if tcp, ok := c.(*net.TCPConn); ok {
		tcp.SetNoDelay(true)
	}
}
