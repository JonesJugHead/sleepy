// Sleepy: sleep/wake wrapper for a Minecraft Java server on Pterodactyl.
package main

import (
	_ "embed"
	"fmt"
	"net"
	"os"
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
)

func main() {
	version := "v" + strings.TrimSpace(versionFile)
	for _, arg := range os.Args[1:] {
		if arg == "--version" || arg == "-v" {
			fmt.Println("Sleepy " + version)
			return
		}
	}
	logx.Printf("Sleepy %s", version)

	cfg, generated, err := config.Load(configFile, true)
	if err != nil {
		logx.Printf("Invalid %s: %v", configFile, err)
		os.Exit(1)
	}
	if generated {
		logx.Printf("%s did not exist: created it with the default settings.", configFile)
		logx.Printf("Review it (backend.command, backend.port...), then start Sleepy again.")
		return
	}
	listenAddress := net.JoinHostPort(cfg.ListenHost, strconv.Itoa(cfg.ListenPort))

	store := settings.Open(cfg, stateFile)
	// v1 has a single backend; ResolveBackend is where routing by handshake address will go.
	b := backend.New("main", cfg.Backend, cfg.StopTimeout)
	// The real MOTD is remembered across restarts for motd.source = "server".
	b.SetServerDescription(store.ServerMotd())
	b.OnDescription = store.SetServerMotd
	idle.Watch(b, store)

	ln, err := net.Listen("tcp", listenAddress)
	if err != nil {
		logx.Printf("Cannot listen on %s: %v", listenAddress, err)
		os.Exit(1)
	}
	// This exact line is the "done" detection string of the Pterodactyl egg.
	logx.Printf("Listening on %s", listenAddress)
	go (&proxy.Proxy{Settings: store, ResolveBackend: func(*protocol.Handshake) *backend.Backend { return b }}).Serve(ln)

	var shutdownOnce sync.Once
	shutdown := func(reason string) {
		started := false
		shutdownOnce.Do(func() {
			started = true
			logx.Printf("Shutting down (%s)", reason)
			ln.Close()
			<-b.Stop("Sleepy is shutting down")
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
		}
		parts = append(parts, fmt.Sprintf("%d player(s) connected", players))
		if autoSleep {
			parts = append(parts, fmt.Sprintf("auto-sleep on (after %v)", cfg.SleepAfter))
		} else {
			parts = append(parts, "auto-sleep OFF")
		}
		switch {
		case !b.Running():
			parts = append(parts, fmt.Sprintf("IP forwarding: %s", b.ProxyProtocolMode()))
		case b.ProxyProtocol():
			parts = append(parts, "real IP forwarded (PROXY protocol)")
		default:
			parts = append(parts, "real IP not forwarded")
		}
		return strings.Join(append(parts, "listening on "+listenAddress), ", ")
	}

	go (&console.Router{
		Backend:  b,
		Settings: store,
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
