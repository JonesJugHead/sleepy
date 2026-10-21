// Package backend runs one Minecraft server: its directory, command, local port and lifecycle.
// Everything that is per-server lives here so several backends can coexist later.
package backend

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
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

const (
	readyPollInterval = time.Second
	// termGrace is the delay between SIGTERM and SIGKILL.
	termGrace = 10 * time.Second
)

type Backend struct {
	Name string

	mu sync.Mutex
	// cfg and stopTimeout are the current settings (reloadable); run is the copy the running
	// process was started with, so a reload never changes the port of a live server.
	cfg         config.Backend
	stopTimeout time.Duration
	run         config.Backend
	// proxyProtocol: the running server expects the PROXY header.
	proxyProtocol bool
	state         State
	active        int // players currently relayed
	startedAt     time.Time
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	exited        chan struct{} // closed once the process is gone
	stopping      chan struct{} // non-nil while the stop sequence runs, closed when it is done
	// restartAfterStop: a player asked to join while stopping, start again once down.
	restartAfterStop bool
	// failures counts failed starts and unexpected exits, so waiters can tell them apart.
	failures uint64
	// changed is closed (and replaced) on every state or player count change.
	changed chan struct{}
	// observer, if set (tests), sees every state transition synchronously.
	observer func(State)
	// description is the real server's MOTD (JSON text component), fetched while it runs.
	description json.RawMessage
	// OnDescription, if set before the first start, is called when the remembered MOTD changes.
	OnDescription func(json.RawMessage)
}

// descriptionRefresh: how often the MOTD is fetched again while the server runs (plugins change it).
const descriptionRefresh = time.Minute

// ServerDescription returns the real server's MOTD as last seen, or nil.
func (b *Backend) ServerDescription() json.RawMessage {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.description
}

// SetServerDescription restores a MOTD remembered from a previous run.
func (b *Backend) SetServerDescription(d json.RawMessage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.description = d
}

func (b *Backend) watchDescription(exited <-chan struct{}) {
	ticker := time.NewTicker(descriptionRefresh)
	defer ticker.Stop()
	for b.State() == Ready {
		if status, ok := FetchStatus(b.Port(), b.ProxyProtocol(), 5*time.Second); ok {
			if d := statusDescription(status); d != nil {
				b.rememberDescription(d)
			}
		}
		select {
		case <-exited:
			return
		case <-ticker.C:
		}
	}
}

func (b *Backend) rememberDescription(d json.RawMessage) {
	b.mu.Lock()
	if bytes.Equal(d, b.description) {
		b.mu.Unlock()
		return
	}
	b.description = d
	callback := b.OnDescription
	b.mu.Unlock()
	if callback != nil {
		callback(d)
	}
}

func New(name string, cfg config.Backend, stopTimeout time.Duration) *Backend {
	return &Backend{Name: name, cfg: cfg, stopTimeout: stopTimeout, state: Sleeping, changed: make(chan struct{})}
}

// SetConfig replaces the settings. A running server keeps its command, port and directory until
// it stops. Returns true if some changes are deferred to the next start for that reason.
func (b *Backend) SetConfig(cfg config.Backend, stopTimeout time.Duration) (deferred bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	deferred = b.cmd != nil && cfg != b.run
	b.cfg, b.stopTimeout = cfg, stopTimeout
	return deferred
}

// current returns the settings in effect: those of the running process, if any.
func (b *Backend) current() config.Backend {
	if b.cmd != nil {
		return b.run
	}
	return b.cfg
}

// Directory is the absolute server directory.
func (b *Backend) Directory() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return absDir(b.current().Directory)
}

// Port is the local port of the server.
func (b *Backend) Port() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.current().Port
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
	return b.cmd != nil && b.proxyProtocol
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

func (b *Backend) ActiveConnections() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.active
}

// Running reports whether a Java process exists (STARTING, READY or STOPPING).
func (b *Backend) Running() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cmd != nil
}

func (b *Backend) Uptime() time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cmd == nil {
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
	fail := func(format string, args ...any) bool {
		logx.Printf(format, args...)
		b.failures++
		b.notifyLocked()
		return false
	}
	cfg := b.cfg
	dir := absDir(cfg.Directory)
	if err := RewriteServerProperties(dir, cfg.Port); err != nil {
		return fail("Cannot update server.properties, not starting: %v", err)
	}
	proxyProtocol, changed, err := ConfigureProxyProtocol(dir, cfg.ProxyProtocol)
	if err != nil {
		return fail("Cannot update %s, not starting: %v", paperGlobalConfig, err)
	}
	if changed {
		logx.Printf("Set proxies.proxy-protocol to %v in %s (backend.proxy_protocol = %q)", proxyProtocol, paperGlobalConfig, cfg.ProxyProtocol)
	}

	logx.Printf("Starting server (%s)", reason)
	// No `exec` prefix: it would break commands starting with VAR=value.
	cmd := exec.Command("sh", "-c", cfg.Command)
	cmd.Dir = dir
	ownProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fail("Cannot run the server command: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fail("Cannot run the server command: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fail("Cannot run the server command: %v", err)
	}
	if err := cmd.Start(); err != nil {
		return fail("Cannot run the server command: %v", err)
	}

	exited := make(chan struct{})
	b.cmd, b.stdin, b.exited, b.startedAt = cmd, stdin, exited, time.Now()
	b.run, b.proxyProtocol = cfg, proxyProtocol
	b.setStateLocked(Starting)

	var copies sync.WaitGroup
	copies.Add(2)
	go func() { defer copies.Done(); b.copyStdout(stdout) }()
	go func() { defer copies.Done(); io.Copy(os.Stderr, stderr) }()
	go func() {
		// The pipes reach EOF once Java itself is gone (it holds them), not merely the sh wrapper.
		copies.Wait()
		err := cmd.Wait()
		b.onExit(cmd, err)
		close(exited)
	}()
	go b.pollReady(exited)
	return true
}

// Stop asks the server to stop: "stop" on its console, then SIGTERM after stop_timeout, then
// SIGKILL. The returned channel is closed once the process is gone. Cancels any pending restart.
func (b *Backend) Stop(reason string) <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.restartAfterStop = false
	if b.stopping != nil {
		return b.stopping
	}
	if b.cmd == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	logx.Printf("Stopping server (%s)", reason)
	b.setStateLocked(Stopping)
	done := make(chan struct{})
	b.stopping = done
	cmd, exited := b.cmd, b.exited
	go func() {
		b.runStopSequence(cmd, exited)
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
	stdin := b.stdin
	b.mu.Unlock()
	if stdin == nil {
		return
	}
	if _, err := io.WriteString(stdin, line+"\n"); err != nil {
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

func (b *Backend) runStopSequence(cmd *exec.Cmd, exited <-chan struct{}) {
	b.mu.Lock()
	stopTimeout := b.stopTimeout
	b.mu.Unlock()
	b.WriteConsole("stop")
	select {
	case <-exited:
		return
	case <-time.After(stopTimeout):
	}
	logx.Printf("Server still running after %v, sending SIGTERM", stopTimeout)
	if err := signalGroup(cmd.Process.Pid, syscall.SIGTERM); err != nil {
		logx.Printf("Cannot send SIGTERM to the server: %v", err)
	}
	select {
	case <-exited:
		return
	case <-time.After(termGrace):
	}
	logx.Printf("Server still running, sending SIGKILL")
	if err := signalGroup(cmd.Process.Pid, syscall.SIGKILL); err != nil {
		logx.Printf("Cannot send SIGKILL to the server: %v", err)
	}
	<-exited
}

// copyStdout copies Java's output as is and watches for the "Done (" line while starting.
func (b *Backend) copyStdout(r io.Reader) {
	var watcher lineWatcher
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			os.Stdout.Write(buf[:n])
			if b.State() == Starting && watcher.feed(buf[:n]) {
				b.markReady("log")
			}
		}
		if err != nil {
			return
		}
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
		if PingStatus(b.Port(), b.ProxyProtocol(), readyPollInterval) {
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
	go b.watchDescription(b.exited)
}

func (b *Backend) onExit(cmd *exec.Cmd, _ error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cmd, b.stdin, b.exited = nil, nil, nil
	how := "unknown status"
	if cmd.ProcessState != nil {
		how = cmd.ProcessState.String()
	}
	if b.state == Stopping {
		logx.Printf("Server stopped (%s)", how)
	} else {
		logx.Printf("Server process exited unexpectedly (%s)", how)
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
