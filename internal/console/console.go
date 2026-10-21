// Package console routes the Pterodactyl console (Sleepy's stdin).
//
// Lines starting with "sleepy" are Sleepy commands and never reach the Minecraft server.
// Otherwise, while Java runs every line goes to Java, so the console behaves exactly as without
// Sleepy ("stop" goes through Backend.Stop to get the SIGTERM/SIGKILL fallback); while asleep,
// start, stop (quit Sleepy), status and help are understood.
package console

import (
	"bufio"
	"io"
	"strings"

	"sleepy/internal/backend"
	"sleepy/internal/logx"
	"sleepy/internal/settings"
)

type Router struct {
	Backend  *backend.Backend
	Settings *settings.Store
	// Reload re-reads sleepy.toml and applies it.
	Reload func() error
	// Quit handles "stop" while the server is asleep: quit Sleepy.
	Quit func()
	// Status returns the text shown by the status commands.
	Status func() string
}

// Run handles lines from in until it ends.
func (r *Router) Run(in io.Reader) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 4096), 1<<20)
	for sc.Scan() {
		r.HandleLine(strings.TrimSuffix(sc.Text(), "\r"))
	}
}

func (r *Router) HandleLine(line string) {
	fields := strings.Fields(line)
	if len(fields) > 0 && strings.EqualFold(fields[0], "sleepy") {
		r.sleepyCommand(fields[1:])
		return
	}

	command := strings.ToLower(strings.TrimSpace(line))
	if r.Backend.Running() {
		if command == "stop" {
			if r.Backend.State() != backend.Stopping {
				r.Backend.Stop("console")
			}
			return
		}
		r.Backend.WriteConsole(line)
		return
	}

	switch command {
	case "":
	case "start":
		r.Backend.Start("console")
	case "stop":
		r.Quit()
	case "status":
		logx.Printf("%s", r.Status())
	case "help":
		r.help()
	default:
		logx.Printf("Unknown command %q: the server is asleep. Type help for the available commands.", command)
	}
}

func (r *Router) sleepyCommand(args []string) {
	sub := ""
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}
	switch sub {
	case "", "help":
		r.help()
	case "status":
		logx.Printf("%s", r.Status())
	case "on":
		r.setAutoSleep(true)
		logx.Printf("Auto-sleep on: the server sleeps after %v without players", r.Settings.Config().SleepAfter)
	case "off":
		r.setAutoSleep(false)
		logx.Printf("Auto-sleep off: the server stays up until \"sleepy on\" (kept across restarts)")
	case "sleep":
		if !r.Backend.Running() {
			logx.Printf("The server is already asleep")
		} else if r.Backend.State() != backend.Stopping {
			r.Backend.Stop("sleepy sleep")
		}
	case "wake":
		if r.Backend.Running() {
			logx.Printf("The server is already %s", strings.ToLower(string(r.Backend.State())))
		} else {
			r.Backend.Start("sleepy wake")
		}
	case "reload":
		if err := r.Reload(); err != nil {
			logx.Printf("Reload failed, keeping the current settings: %v", err)
		}
	default:
		logx.Printf("Unknown command \"sleepy %s\". Type sleepy help for the available commands.", sub)
	}
}

func (r *Router) setAutoSleep(on bool) {
	if err := r.Settings.SetAutoSleep(on); err != nil {
		logx.Printf("Cannot save the auto-sleep state (%v): it will be lost at the next restart", err)
	}
}

func (r *Router) help() {
	logx.Printf("Sleepy commands (never sent to the Minecraft server):")
	logx.Printf("  sleepy status   state, players, auto-sleep, IP forwarding")
	logx.Printf("  sleepy off      keep the server up (auto-sleep off, kept across restarts)")
	logx.Printf("  sleepy on       sleep again after sleep_after without players")
	logx.Printf("  sleepy sleep    stop the server now")
	logx.Printf("  sleepy wake     start the server now")
	logx.Printf("  sleepy reload   re-read sleepy.toml")
	if !r.Backend.Running() {
		logx.Printf("While the server is asleep: start, stop (quit Sleepy), status, help")
	}
}
