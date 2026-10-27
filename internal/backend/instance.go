package backend

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"sleepy/internal/config"
	"sleepy/internal/docker"
	"sleepy/internal/logx"
)

// instance is one run of the server: a process, or a container from start to stop.
type instance interface {
	// Exited is closed once the server is gone.
	Exited() <-chan struct{}
	// ExitStatus says how it ended, once Exited is closed.
	ExitStatus() string
	WriteConsole(line string) error
	// Stop runs the clean stop sequence and returns when it is over (Exited may close later).
	Stop(timeout time.Duration)
}

func address(cfg config.Backend) string {
	return net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
}

// startInstance prepares the server files (process only) and starts the server. proxyProtocol
// tells whether this run expects the PROXY header. onDone is called when the log prints "Done (".
func startInstance(cfg config.Backend, onDone func()) (inst instance, proxyProtocol bool, err error) {
	dir := absDir(cfg.Directory)
	if cfg.Type == config.TypeDocker {
		proxyProtocol, err = ReadProxyProtocol(dir, cfg.ProxyProtocol)
		if err != nil {
			return nil, false, fmt.Errorf("Cannot read %s, not starting: %v", paperGlobalConfig, err)
		}
		inst, err := startContainer(cfg)
		return inst, proxyProtocol, err
	}

	if err := RewriteServerProperties(dir, cfg.Host, cfg.Port); err != nil {
		return nil, false, fmt.Errorf("Cannot update server.properties, not starting: %v", err)
	}
	proxyProtocol, changed, err := ConfigureProxyProtocol(dir, cfg.ProxyProtocol)
	if err != nil {
		return nil, false, fmt.Errorf("Cannot update %s, not starting: %v", paperGlobalConfig, err)
	}
	if changed {
		logx.Printf("Set proxies.proxy-protocol to %v in %s (backend.proxy_protocol = %q)", proxyProtocol, paperGlobalConfig, cfg.ProxyProtocol)
	}
	inst, err = startProcess(cfg.Command, dir, onDone)
	return inst, proxyProtocol, err
}

// adoptInstance tracks a server that is already running (docker only).
func adoptInstance(cfg config.Backend) (instance, bool, error) {
	if cfg.Type != config.TypeDocker {
		return nil, false, errors.New("only containers can be taken over")
	}
	proxyProtocol, err := ReadProxyProtocol(absDir(cfg.Directory), cfg.ProxyProtocol)
	if err != nil {
		return nil, false, err
	}
	client, err := docker.New(cfg.DockerEndpoint)
	if err != nil {
		return nil, false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
	defer cancel()
	state, err := client.Inspect(ctx, cfg.Container)
	if err != nil {
		return nil, false, err
	}
	if !state.Running {
		return nil, false, errors.New("the container is not running")
	}
	return newContainer(client, cfg.Container, state), proxyProtocol, nil
}

// ---- process: Sleepy runs the server command itself ----

// termGrace is the delay between SIGTERM and SIGKILL.
const termGrace = 10 * time.Second

type processInstance struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	exited chan struct{}
}

func startProcess(command, dir string, onDone func()) (*processInstance, error) {
	// No `exec` prefix: it would break commands starting with VAR=value.
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = dir
	ownProcessGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("Cannot run the server command: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("Cannot run the server command: %v", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("Cannot run the server command: %v", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("Cannot run the server command: %v", err)
	}
	p := &processInstance{cmd: cmd, stdin: stdin, exited: make(chan struct{})}
	var copies sync.WaitGroup
	copies.Add(2)
	go func() { defer copies.Done(); copyStdout(stdout, onDone) }()
	go func() { defer copies.Done(); io.Copy(os.Stderr, stderr) }()
	go func() {
		// The pipes reach EOF once Java itself is gone (it holds them), not merely the sh wrapper.
		copies.Wait()
		cmd.Wait()
		close(p.exited)
	}()
	return p, nil
}

func (p *processInstance) Exited() <-chan struct{} { return p.exited }

func (p *processInstance) ExitStatus() string {
	if p.cmd.ProcessState == nil {
		return "unknown status"
	}
	return p.cmd.ProcessState.String()
}

func (p *processInstance) WriteConsole(line string) error {
	_, err := io.WriteString(p.stdin, line+"\n")
	return err
}

// Stop: "stop" on the console, then SIGTERM after timeout, then SIGKILL.
func (p *processInstance) Stop(timeout time.Duration) {
	if err := p.WriteConsole("stop"); err != nil {
		logx.Printf("Cannot write to the server console: %v", err)
	}
	select {
	case <-p.exited:
		return
	case <-time.After(timeout):
	}
	logx.Printf("Server still running after %v, sending SIGTERM", timeout)
	if err := signalGroup(p.cmd.Process.Pid, syscall.SIGTERM); err != nil {
		logx.Printf("Cannot send SIGTERM to the server: %v", err)
	}
	select {
	case <-p.exited:
		return
	case <-time.After(termGrace):
	}
	logx.Printf("Server still running, sending SIGKILL")
	if err := signalGroup(p.cmd.Process.Pid, syscall.SIGKILL); err != nil {
		logx.Printf("Cannot send SIGKILL to the server: %v", err)
	}
	<-p.exited
}

// copyStdout copies Java's output as is and reports the first "Done (" line.
func copyStdout(r io.Reader, onDone func()) {
	var watcher lineWatcher
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			os.Stdout.Write(buf[:n])
			if watcher.feed(buf[:n]) {
				onDone()
			}
		}
		if err != nil {
			return
		}
	}
}

// ---- docker: the server is an existing container ----

// dockerTimeout bounds each Docker API call except stop.
const dockerTimeout = 30 * time.Second

// containerPoll: how often a running container is checked (shorter in tests).
var containerPoll = 2 * time.Second

type containerInstance struct {
	client    *docker.Client
	name      string
	startedAt string
	openStdin bool
	exited    chan struct{}
	// check asks the watcher to look at the container now (after a stop).
	check chan struct{}
	mu    sync.Mutex
	exit  string
}

func startContainer(cfg config.Backend) (*containerInstance, error) {
	client, err := docker.New(cfg.DockerEndpoint)
	if err != nil {
		return nil, fmt.Errorf("Cannot use the Docker API: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
	defer cancel()
	if err := client.Start(ctx, cfg.Container); err != nil {
		return nil, fmt.Errorf("Cannot start container %q (%s): %v", cfg.Container, client.Endpoint(), err)
	}
	state, err := client.Inspect(ctx, cfg.Container)
	if err != nil {
		return nil, fmt.Errorf("Cannot inspect container %q after starting it: %v", cfg.Container, err)
	}
	return newContainer(client, cfg.Container, state), nil
}

func newContainer(client *docker.Client, name string, state docker.State) *containerInstance {
	c := &containerInstance{client: client, name: name, startedAt: state.StartedAt, openStdin: state.OpenStdin,
		exited: make(chan struct{}), check: make(chan struct{}, 1)}
	if !state.Running {
		c.exit = fmt.Sprintf("exit code %d", state.ExitCode)
		close(c.exited)
		return c
	}
	go c.watch()
	return c
}

// watch closes exited once the container stops, or restarts (a new run is not this instance).
func (c *containerInstance) watch() {
	lastErr := ""
	ticker := time.NewTicker(containerPoll)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-c.check:
		}
		ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
		state, err := c.client.Inspect(ctx, c.name)
		cancel()
		switch {
		case errors.Is(err, docker.ErrNotFound):
			c.finish("container removed")
			return
		case err != nil:
			// The API may be briefly unreachable: the container is not assumed gone.
			if err.Error() != lastErr {
				logx.Printf("Cannot check container %q: %v", c.name, err)
				lastErr = err.Error()
			}
			continue
		case !state.Running:
			c.finish(fmt.Sprintf("exit code %d", state.ExitCode))
			return
		case state.StartedAt != c.startedAt:
			c.finish("restarted outside Sleepy")
			return
		}
		lastErr = ""
	}
}

func (c *containerInstance) finish(status string) {
	c.mu.Lock()
	c.exit = status
	c.mu.Unlock()
	close(c.exited)
}

func (c *containerInstance) Exited() <-chan struct{} { return c.exited }

func (c *containerInstance) ExitStatus() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.exit
}

func (c *containerInstance) WriteConsole(line string) error {
	if !c.openStdin {
		return fmt.Errorf("container %q has no open stdin (stdin_open: true), commands cannot reach it", c.name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return c.client.WriteStdin(ctx, c.name, line+"\n")
}

// Stop is a Docker stop: the container's stop signal (SIGTERM), then SIGKILL by Docker after timeout.
func (c *containerInstance) Stop(timeout time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout+dockerTimeout)
	defer cancel()
	if err := c.client.Stop(ctx, c.name, timeout); err != nil && !errors.Is(err, docker.ErrNotFound) {
		logx.Printf("Cannot stop container %q: %v", c.name, err)
	}
	select {
	case c.check <- struct{}{}:
	default:
	}
}

// WatchExternalStarts adopts the container when it is started outside Sleepy (docker type), and
// once at launch if it already runs. Checks every interval while the server is asleep.
func (b *Backend) WatchExternalStarts(interval time.Duration) {
	lastErr := ""
	for first := true; ; first = false {
		if !first {
			time.Sleep(interval)
		}
		b.mu.Lock()
		cfg, idle := b.cfg, b.inst == nil && b.state == Sleeping
		b.mu.Unlock()
		if cfg.Type != config.TypeDocker || !idle {
			continue
		}
		client, err := docker.New(cfg.DockerEndpoint)
		var state docker.State
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
			state, err = client.Inspect(ctx, cfg.Container)
			cancel()
		}
		if err != nil {
			if msg := err.Error(); msg != lastErr {
				logx.Printf("Cannot check container %q: %v", cfg.Container, err)
				lastErr = msg
			}
			continue
		}
		if lastErr != "" || first {
			logx.Printf("Docker: container %q on %s is %s", cfg.Container, client.Endpoint(), runningWord(state.Running))
			lastErr = ""
		}
		if state.Running {
			b.Adopt()
		}
	}
}

func runningWord(running bool) string {
	if running {
		return "running"
	}
	return "stopped"
}
