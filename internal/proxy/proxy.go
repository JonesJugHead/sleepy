// Package proxy listens on the public port, answers pings while the server sleeps, wakes it up
// for players and relays their connections.
package proxy

import (
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"time"

	"sleepy/internal/backend"
	"sleepy/internal/logx"
	"sleepy/internal/protocol"
	"sleepy/internal/settings"
)

const (
	// handshakeTimeout bounds the handshake and the login start / status exchange.
	handshakeTimeout = 10 * time.Second
	// legacyWait: a lone 0xFE may be followed by more bytes from a 1.4+ client.
	legacyWait = 200 * time.Millisecond
)

type Proxy struct {
	// Settings is read on every connection, so a reload applies to the next ones.
	Settings *settings.Store
	// ResolveBackend picks the backend for a connection (nil handshake for legacy pings).
	// Single backend in v1; routing by handshake address goes here later.
	ResolveBackend func(*protocol.Handshake) *backend.Backend
}

// Serve accepts connections until the listener is closed.
func (p *Proxy) Serve(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			logx.Printf("Accept failed: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}
		go p.handle(conn)
	}
}

var protocolErrorf = protocol.Errorf

func (p *Proxy) handle(conn net.Conn) {
	defer func() {
		if r := recover(); r != nil {
			logx.Printf("Unexpected error on a connection: %v", r)
		}
	}()
	defer conn.Close()
	setNoDelay(conn)
	c := newClientConn(conn)
	peer := conn.RemoteAddr().String()
	c.SetReadDeadline(time.Now().Add(handshakeTimeout))

	if err := p.dispatch(c, peer); err != nil && !isQuietError(err) {
		var pe *protocol.Error
		if errors.As(err, &pe) {
			logx.Printf("Protocol error from %s: %v", peer, err)
		} else {
			logx.Printf("Error from %s: %v", peer, err)
		}
	}
}

// isQuietError: clients closing early and idle connections (port scanners) are normal.
func isQuietError(err error) bool {
	var ne net.Error
	return errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.EPIPE) || (errors.As(err, &ne) && ne.Timeout())
}

func (p *Proxy) dispatch(c *clientConn, peer string) error {
	for len(c.reader.Buffered()) == 0 {
		if err := c.fill(); err != nil {
			return err
		}
	}
	if c.reader.Buffered()[0] == protocol.LegacyPingByte {
		return p.handleLegacyPing(c)
	}

	hsPacket, err := c.next()
	if err != nil {
		return err
	}
	hs, err := protocol.DecodeHandshake(hsPacket)
	if err != nil {
		return err
	}
	b := p.ResolveBackend(&hs)

	switch hs.Intent {
	case protocol.IntentStatus:
		// A server list ping never wakes the server up.
		if b.State() == backend.Ready {
			c.SetReadDeadline(time.Time{})
			relay(c.Conn, concat(hsPacket.Raw, c.reader.Drain()), b, "", nil)
			return nil
		}
		return serveStatus(c, localStatusJSON(b, p.Settings.Config().Motd, hs.ProtocolVersion))

	case protocol.IntentLogin, protocol.IntentTransfer:
		loginPacket, err := c.next()
		if err != nil {
			return err
		}
		username, err := protocol.DecodeLoginStart(loginPacket)
		if err != nil {
			return err
		}
		p.handleLogin(c, b, username, peer, func() []byte { return concat(hsPacket.Raw, loginPacket.Raw, c.reader.Drain()) })
		return nil
	}
	return protocolErrorf("unknown handshake intent %d", hs.Intent)
}

func (p *Proxy) handleLogin(c *clientConn, b *backend.Backend, username, peer string, replay func() []byte) {
	cfg := p.Settings.Config()
	kick := protocol.EncodeLoginDisconnect(cfg.KickMessage)

	if b.State() == backend.Ready {
		c.SetReadDeadline(time.Time{})
		relay(c.Conn, replay(), b, username, kick)
		return
	}

	// whitelist_only only decides who may wake the server; once it is up, its own whitelist applies.
	if cfg.WhitelistOnly && b.State() != backend.Starting && !backend.IsWhitelisted(b.Directory(), username) {
		logx.Printf("%s (%s) is not in whitelist.json, the server stays asleep", username, peer)
		c.Write(kick)
		return
	}

	logx.Printf("%s (%s) is waiting for the server", username, peer)
	if !b.Start(username + " is connecting") {
		c.Write(kick)
		return
	}

	closed, stopWatching := c.watchClose()
	ready := b.WaitReady(cfg.HoldTimeout, closed)
	stopWatching()
	select {
	case <-closed:
		logx.Printf("%s left before the server was ready", username)
		return
	default:
	}
	if !ready {
		why := fmt.Sprintf("Server not ready within %v", cfg.HoldTimeout)
		if b.State() == backend.Sleeping {
			why = "Server failed to start"
		}
		logx.Printf("%s, asking %s to reconnect", why, username)
		c.Write(kick)
		return
	}
	relay(c.Conn, replay(), b, username, kick)
}

func (p *Proxy) handleLegacyPing(c *clientConn) error {
	c.SetReadDeadline(time.Now().Add(legacyWait))
	ping, ok := protocol.ClassifyLegacyPing(c.reader.Buffered(), false)
	for !ok {
		err := c.fill()
		if err != nil && !isTimeout(err) {
			return err // client gone
		}
		ping, ok = protocol.ClassifyLegacyPing(c.reader.Buffered(), err != nil)
	}
	c.SetReadDeadline(time.Now().Add(handshakeTimeout))

	b := p.ResolveBackend(nil)
	if b.State() == backend.Ready {
		c.SetReadDeadline(time.Time{})
		relay(c.Conn, c.reader.Drain(), b, "", nil)
		return nil
	}
	_, err := c.Write(protocol.EncodeLegacyResponse(ping, localLegacyStatus(b, p.Settings.Config().Motd)))
	return err
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
