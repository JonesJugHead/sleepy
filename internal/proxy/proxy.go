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
	"sleepy/internal/config"
	"sleepy/internal/logx"
	"sleepy/internal/power"
	"sleepy/internal/protocol"
	"sleepy/internal/settings"
)

const (
	// handshakeTimeout bounds the handshake and the login start / status exchange.
	handshakeTimeout = 10 * time.Second
	// legacyWait: a lone 0xFE may be followed by more bytes from a 1.4+ client.
	legacyWait = 200 * time.Millisecond
	// keepAliveChannel is unknown to clients, which answer "not understood".
	keepAliveChannel = "sleepy:wait"
	// legacyHoldLimit: clients before 1.13 cannot be kept waiting past their 30 s timeout.
	legacyHoldLimit = 25 * time.Second
	// answersTimeout: how long the last keep-alive answers may take once the server is ready.
	answersTimeout = 5 * time.Second
)

type Proxy struct {
	// Settings is read on every connection, so a reload applies to the next ones.
	Settings *settings.Store
	// ResolveBackend picks the backend for a connection (nil handshake for legacy pings).
	// Single backend in v1; routing by handshake address goes here later.
	ResolveBackend func(*protocol.Handshake) *backend.Backend
	// Power is the shared state directory ([power]); nil when off.
	Power *power.Dir
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

// keepAliveInterval: how often a waiting 1.13+ client gets a login plugin request, well within
// its 30 s read timeout (shorter in tests).
var keepAliveInterval = 10 * time.Second

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
		return serveStatus(c, localStatusJSON(b, p.Settings.Config().Motd, p.Power.AdminStopped(), hs.ProtocolVersion))

	case protocol.IntentLogin, protocol.IntentTransfer:
		loginPacket, err := c.next()
		if err != nil {
			return err
		}
		username, err := protocol.DecodeLoginStart(loginPacket)
		if err != nil {
			return err
		}
		p.handleLogin(c, b, hs.ProtocolVersion, username, peer, concat(hsPacket.Raw, loginPacket.Raw))
		return nil
	}
	return protocolErrorf("unknown handshake intent %d", hs.Intent)
}

// handleLogin relays a player, waking the server up first if needed. prefix holds the handshake and
// login start, replayed to the server before anything else the client sent.
func (p *Proxy) handleLogin(c *clientConn, b *backend.Backend, protocolVersion int32, username, peer string, prefix []byte) {
	cfg := p.Settings.Config()
	kick := protocol.EncodeLoginDisconnect(cfg.KickMessage)

	if b.State() == backend.Ready {
		c.SetReadDeadline(time.Time{})
		relay(c.Conn, concat(prefix, c.reader.Drain()), b, username, kick)
		return
	}

	// whitelist_only only decides who may wake the server; once it is up, its own whitelist applies.
	if cfg.WhitelistOnly && b.State() != backend.Starting && !backend.IsWhitelisted(b.Directory(), username) {
		logx.Printf("%s (%s) is not in whitelist.json, the server stays asleep", username, peer)
		c.Write(kick)
		return
	}

	// A server that is down or going down is only (re)started if the power directory allows it.
	if state := b.State(); state == backend.Sleeping || state == backend.Stopping {
		if why := p.Power.WakeBlocked(); why != power.None {
			logx.Printf("%s (%s) cannot wake the server: %s", username, peer, power.Describe(why))
			c.Write(protocol.EncodeLoginDisconnect(blockedMessage(cfg.Power, why)))
			return
		}
	}

	logx.Printf("%s (%s) is waiting for the server", username, peer)
	if !b.Start(username + " is connecting") {
		c.Write(kick)
		return
	}

	keepAlive := protocolVersion >= protocol.LoginPluginMinProtocol
	hold := cfg.HoldTimeout
	if !keepAlive && hold > legacyHoldLimit {
		hold = legacyHoldLimit
	}
	ready, left, sent := p.wait(c, b, hold, keepAlive)
	if left {
		logx.Printf("%s left before the server was ready", username)
		return
	}
	if !ready {
		why := fmt.Sprintf("Server not ready within %v", hold)
		if b.State() == backend.Sleeping {
			why = "Server failed to start"
		}
		logx.Printf("%s, asking %s to reconnect", why, username)
		c.Write(kick)
		return
	}
	rest, err := c.takeKeepAliveAnswers(sent, answersTimeout)
	if err != nil {
		logx.Printf("%s did not answer while waiting (%v), asking to reconnect", username, err)
		c.Write(kick)
		return
	}
	relay(c.Conn, concat(prefix, rest), b, username, kick)
}

// wait holds a player until the server is ready, the hold expires, the server fails or the player
// leaves. With keepAlive, a login plugin request is sent every keepAliveInterval; sent is how many.
func (p *Proxy) wait(c *clientConn, b *backend.Backend, hold time.Duration, keepAlive bool) (ready, left bool, sent int32) {
	deadline := time.Now().Add(hold)
	closed, stopWatching := c.watchClose()
	defer stopWatching()
	for {
		step := time.Until(deadline)
		if step <= 0 {
			return false, false, sent
		}
		if keepAlive && step > keepAliveInterval {
			step = keepAliveInterval
		}
		if b.WaitReady(step, closed) {
			return true, false, sent
		}
		select {
		case <-closed:
			return false, true, sent
		default:
		}
		if b.State() == backend.Sleeping {
			return false, false, sent // the start failed
		}
		if keepAlive && time.Until(deadline) > 0 {
			sent++
			if _, err := c.Write(protocol.EncodeLoginPluginRequest(sent, keepAliveChannel)); err != nil {
				return false, true, sent
			}
		}
	}
}

func blockedMessage(cfg config.Power, why string) string {
	switch why {
	case power.Backup:
		return cfg.BackupMessage
	case power.Restore:
		return cfg.RestoreMessage
	}
	return cfg.StoppedMessage
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
	_, err := c.Write(protocol.EncodeLegacyResponse(ping, localLegacyStatus(b, p.Settings.Config().Motd, p.Power.AdminStopped())))
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
