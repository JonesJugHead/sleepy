// Package backend runs one Minecraft server: its address, how it is started and stopped, and its
// lifecycle. The server is either a process Sleepy runs itself or a Docker container (instance.go).
// Everything that is per-server lives here so several backends can coexist later.
package backend

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"sync"
	"time"

	"sleepy/internal/config"
	"sleepy/internal/logx"
)

type State string

const (
	Sleeping State = "SLEEPING"
	Starting State = "STARTING"
	Ready    State = "READY"
	Stopping State = "STOPPING"
)

const readyPollInterval = time.Second

// statusRefresh: how often the server's status is fetched again while it runs (plugins change the
// MOTD); statusRetry: until the first success.
const (
	statusRefresh = time.Minute
	statusRetry   = 5 * time.Second
)

// ServerStatus is what Sleepy remembers of the real server to show it while it sleeps.
type ServerStatus struct {
	// Description is the MOTD (JSON text component).
	Description json.RawMessage `json:"description,omitempty"`
	Favicon     string          `json:"favicon,omitempty"`
	MaxPlayers  int             `json:"max_players,omitempty"`
}

type Backend struct {
	Name string

	mu sync.Mutex
	// cfg and stopTimeout are the current settings (reloadable); run is the copy the running
	// server was started with, so a reload never changes the address of a live server.
	cfg         config.Backend
	stopTimeout time.Duration
	run         config.Backend
	// proxyProtocol: the running server expects the PROXY header.
	proxyProtocol bool
	state         State
	active        int // players currently relayed
	startedAt     time.Time
	inst          instance
	exited        chan struct{} // closed once the running server is gone
	stopping      chan struct{} // non-nil while the stop sequence runs, closed when it is done
	// external: the running server was started outside Sleepy (docker), Sleepy only adopted it.
	external bool
	// restartAfterStop: a player asked to join while stopping, start again once down.
	restartAfterStop bool
	// failures counts failed starts and unexpected exits, so waiters can tell them apart.
	failures uint64
	// changed is closed (and replaced) on every state or player count change.
	changed chan struct{}
	// observer, if set (tests), sees every state transition synchronously.
	observer func(State)
	status   ServerStatus
	// OnStatus, if set before the first start, is called when the remembered status changes.
	OnStatus func(ServerStatus)
}

func New(name string, cfg config.Backend, stopTimeout time.Duration) *Backend {
	return &Backend{Name: name, cfg: cfg, stopTimeout: stopTimeout, state: Sleeping, changed: make(chan struct{})}
}

// SetConfig replaces the settings. A running server keeps its type, address and directory until
// it stops. Returns true if some changes are deferred to the next start for that reason.
func (b *Backend) SetConfig(cfg config.Backend, stopTimeout time.Duration) (deferred bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	deferred = b.inst != nil && cfg != b.run
	b.cfg, b.stopTimeout = cfg, stopTimeout
	return deferred
}

// current returns the settings in effect: those of the running server, if any.
func (b *Backend) current() config.Backend {
	if b.inst != nil {
		return b.run
	}
	return b.cfg
}

// Type is the backend type in effect (config.TypeProcess or config.TypeDocker).
func (b *Backend) Type() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.current().Type
}

// Directory is the absolute server directory.
func (b *Backend) Directory() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return absDir(b.current().Directory)
}

// Address is where the server is reached, as host:port.
func (b *Backend) Address() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return address(b.current())
}

// ProxyProtocolMode is the configured backend.proxy_protocol ("auto", "on", "off").
func (b *Backend) ProxyProtocolMode() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cfg.ProxyProtocol
}

// ProxyProtocol reports whether the running server expects the PROXY header.
func (b *Backend) ProxyProtocol() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inst != nil && b.proxyProtocol
}

func absDir(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

// Snapshot returns the state, the player count and a channel closed at the next change.
func (b *Backend) Snapshot() (State, int, <-chan struct{}) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state, b.active, b.changed
}

func (b *Backend) State() State {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.state
}

// External reports whether the running server was started outside Sleepy.
func (b *Backend) External() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inst != nil && b.external
}

func (b *Backend) ActiveConnections() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active
}

// Running reports whether the server exists (STARTING, READY or STOPPING).
func (b *Backend) Running() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.inst != nil
}

func (b *Backend) Uptime() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.inst == nil {
		return 0
	}
	return time.Since(b.startedAt)
}

// Start starts the server if needed. It returns false only if the server cannot come up.
func (b *Backend) Start(reason string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case Starting, Ready:
		return true
	case Stopping:
		if !b.restartAfterStop {
			logx.Printf("Server is stopping, it will start again right after (%s)", reason)
		}
		b.restartAfterStop = true
		return true
	}
	return b.startLocked(reason)
}

func (b *Backend) startLocked(reason string) bool {
	cfg := b.cfg
	logx.Printf("Starting server (%s)", reason)
	inst, proxyProtocol, err := startInstance(cfg, b.onDoneLine)
	if err != nil {
		logx.Printf("%v", err)
		b.failures++
		b.notifyLocked()
		return false
	}
	b.runLocked(cfg, inst, proxyProtocol, false)
	return true
}

// runLocked tracks a server that has just been started or adopted.
func (b *Backend) runLocked(cfg config.Backend, inst instance, proxyProtocol, external bool) {
	exited := make(chan struct{})
	b.inst, b.exited, b.startedAt, b.external = inst, exited, time.Now(), external
	b.run, b.proxyProtocol = cfg, proxyProtocol
	b.setStateLocked(Starting)
	go func() {
		<-inst.Exited()
		b.onExit(inst)
		close(exited)
	}()
	go b.pollReady(exited)
}

// Adopt takes over a server started outside Sleepy (docker: by a panel or by hand), so that it is
// shown as awake and put to sleep like any other. Returns false if a server is already tracked.
func (b *Backend) Adopt() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.inst != nil || b.state != Sleeping {
		return false
	}
	cfg := b.cfg
	inst, proxyProtocol, err := adoptInstance(cfg)
	if err != nil {
		logx.Printf("Cannot take over the running server: %v", err)
		return false
	}
	logx.Printf("The server was started outside Sleepy, now managing it")
	b.runLocked(cfg, inst, proxyProtocol, true)
	return true
}

// Stop asks the server to stop cleanly (instance.go has the sequence of each type). The returned
// channel is closed once the server is gone. Cancels any pending restart.
func (b *Backend) Stop(reason string) <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.restartAfterStop = false
	if b.stopping != nil {
		return b.stopping
	}
	if b.inst == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	logx.Printf("Stopping server (%s)", reason)
	b.setStateLocked(Stopping)
	done := make(chan struct{})
	b.stopping = done
	inst, exited, stopTimeout := b.inst, b.exited, b.stopTimeout
	go func() {
		inst.Stop(stopTimeout)
		<-exited
		b.mu.Lock()
		b.stopping = nil
		b.mu.Unlock()
		close(done)
	}()
	return done
}

// WaitReady returns true as soon as the server is READY, false on failure, timeout or cancel.
func (b *Backend) WaitReady(timeout time.Duration, cancel <-chan struct{}) bool {
	b.mu.Lock()
	failuresBefore := b.failures
	b.mu.Unlock()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		b.mu.Lock()
		state, failures, changed := b.state, b.failures, b.changed
		b.mu.Unlock()
		if state == Ready {
			return true
		}
		if failures != failuresBefore {
			return false
		}
		select {
		case <-changed:
		case <-timer.C:
			return false
		case <-cancel:
			return false
		}
	}
}

// WriteConsole sends one line to the server console.
func (b *Backend) WriteConsole(line string) {
	b.mu.Lock()
	inst := b.inst
	b.mu.Unlock()
	if inst == nil {
		return
	}
	if err := inst.WriteConsole(line); err != nil {
		logx.Printf("Cannot write to the server console: %v", err)
	}
}

func (b *Backend) Acquire() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.active++
	b.notifyLocked()
	return b.active
}

func (b *Backend) Release() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.active > 0 {
		b.active--
	}
	b.notifyLocked()
	return b.active
}

// onDoneLine is called by a process instance when its log prints the "Done (" line.
func (b *Backend) onDoneLine() {
	if b.State() == Starting {
		b.markReady("log")
	}
}

func (b *Backend) pollReady(exited <-chan struct{}) {
	ticker := time.NewTicker(readyPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-exited:
			return
		case <-ticker.C:
		}
		if b.State() != Starting {
			return
		}
		if PingStatus(b.Address(), b.ProxyProtocol(), readyPollInterval) {
			b.markReady("status ping")
			return
		}
	}
}

func (b *Backend) markReady(detectedBy string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state != Starting {
		return
	}
	logx.Printf("Server is ready after %.1fs (detected by %s)", time.Since(b.startedAt).Seconds(), detectedBy)
	b.setStateLocked(Ready)
	go b.watchStatus(b.exited)
}

// RememberedStatus returns what was last seen of the real server's status.
func (b *Backend) RememberedStatus() ServerStatus {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.status
}

// SetRememberedStatus restores a status remembered from a previous run.
func (b *Backend) SetRememberedStatus(s ServerStatus) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status = s
}

func (b *Backend) watchStatus(exited <-chan struct{}) {
	wait := statusRetry
	for b.State() == Ready {
		if js, ok := FetchStatus(b.Address(), b.ProxyProtocol(), 5*time.Second); ok {
			if s, ok := parseServerStatus(js); ok {
				b.rememberStatus(s)
				wait = statusRefresh
			}
		}
		select {
		case <-exited:
			return
		case <-time.After(wait):
		}
	}
}

func (b *Backend) rememberStatus(s ServerStatus) {
	b.mu.Lock()
	if bytes.Equal(s.Description, b.status.Description) && s.Favicon == b.status.Favicon && s.MaxPlayers == b.status.MaxPlayers {
		b.mu.Unlock()
		return
	}
	b.status = s
	callback := b.OnStatus
	b.mu.Unlock()
	if callback != nil {
		callback(s)
	}
}

func (b *Backend) onExit(inst instance) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.inst, b.exited, b.external = nil, nil, false
	if b.state == Stopping {
		logx.Printf("Server stopped (%s)", inst.ExitStatus())
	} else {
		logx.Printf("Server exited unexpectedly (%s)", inst.ExitStatus())
		b.failures++
	}
	b.setStateLocked(Sleeping)
	if b.restartAfterStop {
		b.restartAfterStop = false
		b.startLocked("player waiting")
	}
}

func (b *Backend) notifyLocked() {
	close(b.changed)
	b.changed = make(chan struct{})
}

func (b *Backend) setStateLocked(s State) {
	if b.state != s {
		b.state = s
		b.notifyLocked()
		if b.observer != nil {
			b.observer(s)
		}
	}
}
