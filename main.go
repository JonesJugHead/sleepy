// Sleepy: sleep/wake wrapper for a Minecraft Java server, run as a process (Pterodactyl...) or as
// a Docker container.
package main

import (
	"context"
	_ "embed"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"sleepy/internal/backend"
	"sleepy/internal/config"
	"sleepy/internal/console"
	"sleepy/internal/idle"
	"sleepy/internal/logx"
	"sleepy/internal/power"
	"sleepy/internal/protocol"
	"sleepy/internal/proxy"
	"sleepy/internal/settings"
)

// The version is defined only in the VERSION file.
//
//go:embed VERSION
var versionFile string

const (
	configFile = "sleepy.toml"
	// stateFile keeps what the console commands change (auto-sleep) across restarts.
	stateFile = "sleepy-state.json"
	// externalStartCheck: how often a sleeping docker backend is checked for a start outside Sleepy.
	externalStartCheck = 5 * time.Second
	// heartbeat: sleepy.json is rewritten at least this often ([power]).
	heartbeat = 30 * time.Second
	// busyCommandTimeout bounds busy_command; a command that hangs counts as "not busy".
	busyCommandTimeout = time.Minute
)

func main() {
	version := strings.TrimSpace(versionFile)
	for _, arg := range os.Args[1:] {
		if arg == "--version" || arg == "-v" {
			fmt.Println("Sleepy v" + version)
			return
		}
	}
	logx.Printf("Sleepy v%s", version)

	cfg, generated, err := config.Load(configFile, true)
	if err != nil {
		logx.Printf("Invalid %s: %v", configFile, err)
		os.Exit(1)
	}
	if generated {
		logx.Printf("%s did not exist: created it with the default settings for backend.type = %q.", configFile, config.DetectType())
		logx.Printf("Review it (backend.type, backend.address, backend.command or backend.container...), then start Sleepy again.")
		return
	}
	listenAddress := net.JoinHostPort(cfg.ListenHost, strconv.Itoa(cfg.ListenPort))

	store := settings.Open(cfg, stateFile)
	powerDir := power.Open(cfg.Power.Directory)
	// v1 has a single backend; ResolveBackend is where routing by handshake address will go.
	b := backend.New("main", cfg.Backend, cfg.StopTimeout)
	// The real server's status is remembered across restarts, to be shown while it sleeps.
	b.SetRememberedStatus(store.ServerStatus())
	b.OnStatus = store.SetServerStatus
	idle.Watch(b, store, idle.Guards{
		KeepAwake: powerDir.KeepAwake,
		Busy:      func() (bool, string) { return runBusyCommand(store.Config().BusyCommand) },
	})
	logx.Printf("Backend: %s", describeBackend(cfg.Backend))
	if powerDir != nil {
		logx.Printf("Power directory: %s", powerDir.Path())
		go writePowerState(powerDir, b, version)
	}
	go b.WatchExternalStarts(externalStartCheck)

	ln, err := net.Listen("tcp", listenAddress)
	if err != nil {
		logx.Printf("Cannot listen on %s: %v", listenAddress, err)
		os.Exit(1)
	}
	// This exact line is the "done" detection string of the Pterodactyl egg.
	logx.Printf("Listening on %s", listenAddress)
	go (&proxy.Proxy{Settings: store, Power: powerDir, ResolveBackend: func(*protocol.Handshake) *backend.Backend { return b }}).Serve(ln)

	var shutdownOnce sync.Once
	shutdown := func(reason string) {
		started := false
		shutdownOnce.Do(func() {
			started = true
			logx.Printf("Shutting down (%s)", reason)
			ln.Close()
			// A container lives on its own: Sleepy takes it over again when it comes back.
			if b.Type() == config.TypeDocker {
				if b.Running() {
					logx.Printf("Leaving container %q running", cfg.Backend.Container)
				}
			} else {
				<-b.Stop("Sleepy is shutting down")
			}
			os.Exit(0)
		})
		if !started {
			logx.Printf("Already shutting down, waiting for the server to stop")
		}
	}

	reload := func() error {
		next, _, err := config.Load(configFile, false)
		if err != nil {
			return err
		}
		if next.ListenHost != cfg.ListenHost || next.ListenPort != cfg.ListenPort {
			logx.Printf("public_address changed: restart Sleepy to listen on the new address")
		}
		if next.Power.Directory != cfg.Power.Directory {
			logx.Printf("power.directory changed: restart Sleepy to use the new directory")
		}
		store.SetConfig(next)
		if b.SetConfig(next.Backend, next.StopTimeout) {
			logx.Printf("Reloaded %s (backend changes apply at the next server start)", configFile)
		} else {
			logx.Printf("Reloaded %s", configFile)
		}
		return nil
	}

	status := func() string {
		cfg, autoSleep, _ := store.Snapshot()
		state, players, _ := b.Snapshot()
		parts := []string{fmt.Sprintf("Server %s", state)}
		if b.Running() {
			parts[0] += fmt.Sprintf(" (up for %v)", b.Uptime().Round(time.Second))
			if b.External() {
				parts[0] += ", started outside Sleepy"
			}
		}
		parts = append(parts, fmt.Sprintf("%d player(s) connected", players))
		if autoSleep {
			parts = append(parts, fmt.Sprintf("auto-sleep on (after %v)", cfg.SleepAfter))
		} else {
			parts = append(parts, "auto-sleep OFF")
		}
		if why := powerDir.WakeBlocked(); why != power.None && !b.Running() {
			parts = append(parts, "cannot wake: "+power.Describe(why))
		}
		switch {
		case !b.Running():
			parts = append(parts, fmt.Sprintf("IP forwarding: %s", b.ProxyProtocolMode()))
		case b.ProxyProtocol():
			parts = append(parts, "real IP forwarded (PROXY protocol)")
		default:
			parts = append(parts, "real IP not forwarded")
		}
		parts = append(parts, describeBackend(cfg.Backend))
		return strings.Join(append(parts, "listening on "+listenAddress), ", ")
	}

	go (&console.Router{
		Backend:  b,
		Settings: store,
		Power:    powerDir,
		Reload:   reload,
		Quit:     func() { go shutdown("stop command") },
		Status:   status,
	}).Run(os.Stdin)

	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	for sig := range signals {
		go shutdown(sig.String())
	}
}

func describeBackend(cfg config.Backend) string {
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	if cfg.Type == config.TypeDocker {
		return fmt.Sprintf("docker container %q at %s", cfg.Container, addr)
	}
	return fmt.Sprintf("process at %s", addr)
}

// runBusyCommand runs busy_command: exit code 0 means busy, and its first output line says why.
func runBusyCommand(command string) (bool, string) {
	if command == "" {
		return false, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), busyCommandTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "sh", "-c", command).Output()
	if ctx.Err() != nil {
		logx.Printf("busy_command did not finish within %v, not treated as busy", busyCommandTimeout)
		return false, ""
	}
	if err != nil {
		return false, ""
	}
	reason, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	if reason == "" {
		reason = "busy_command reports work in progress"
	}
	return true, reason
}

// writePowerState keeps sleepy.json up to date: on every change, and at least every heartbeat.
func writePowerState(dir *power.Dir, b *backend.Backend, version string) {
	last, lastErr := "", ""
	var since time.Time
	for {
		state, _, changed := b.Snapshot()
		current := powerState(state, b.External())
		if current != last {
			last, since = current, time.Now()
		}
		if err := dir.WriteState(current, since, version); err != nil {
			if err.Error() != lastErr {
				logx.Printf("Cannot write sleepy.json: %v", err)
				lastErr = err.Error()
			}
		} else {
			lastErr = ""
		}
		select {
		case <-changed:
		case <-time.After(heartbeat):
		}
	}
}

// powerState maps the backend state to sleepy.json: "waking" is a start Sleepy triggered.
func powerState(state backend.State, external bool) string {
	switch {
	case state == backend.Sleeping:
		return power.SleepingState
	case state == backend.Starting && !external:
		return power.Waking
	}
	return power.Awake
}
