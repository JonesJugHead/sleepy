package idle

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"sleepy/internal/backend"
	"sleepy/internal/config"
	"sleepy/internal/console"
	"sleepy/internal/settings"
	"sleepy/internal/testutil"
)

// The console router is tested here too: both need a running fake server.

func TestMain(m *testing.M) {
	testutil.MaybeRunFakeServer()
	os.Exit(m.Run())
}

func newBackend(t *testing.T) *backend.Backend {
	t.Helper()
	b := backend.New("test", config.Backend{Command: testutil.FakeServerCommand(nil), Port: testutil.FreePort(t), Directory: t.TempDir(), ProxyProtocol: "auto"}, 5*time.Second)
	t.Cleanup(func() { <-b.Stop("test cleanup") })
	return b
}

func newStore(t *testing.T, sleepAfter, minimumOnline time.Duration) (*settings.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sleepy-state.json")
	return settings.Open(&config.Config{SleepAfter: sleepAfter, MinimumOnlineTime: minimumOnline}, path), path
}

func TestStopsAfterSleepAfter(t *testing.T) {
	b := newBackend(t)
	store, _ := newStore(t, 300*time.Millisecond, 0)
	Watch(b, store)
	b.Start("test")
	b.WaitReady(5*time.Second, nil)
	b.Acquire()
	time.Sleep(500 * time.Millisecond)
	if b.State() != backend.Ready {
		t.Fatal("stopped while a player is connected")
	}
	b.Release()
	testutil.Until(t, 3*time.Second, func() bool { return b.State() == backend.Sleeping })
}

func TestHonoursMinimumOnlineTime(t *testing.T) {
	b := newBackend(t)
	store, _ := newStore(t, 100*time.Millisecond, 1200*time.Millisecond)
	Watch(b, store)
	b.Start("test")
	b.WaitReady(5*time.Second, nil)
	var uptimeAtStop time.Duration
	testutil.Until(t, 4*time.Second, func() bool {
		if b.State() == backend.Stopping && uptimeAtStop == 0 {
			uptimeAtStop = b.Uptime()
		}
		return b.State() == backend.Sleeping
	})
	if uptimeAtStop < 1150*time.Millisecond {
		t.Fatalf("stopped after %v", uptimeAtStop)
	}
}

func TestAutoSleepOffKeepsTheServerUp(t *testing.T) {
	b := newBackend(t)
	store, path := newStore(t, 200*time.Millisecond, 0)
	store.SetAutoSleep(false)
	Watch(b, store)
	b.Start("test")
	b.WaitReady(5*time.Second, nil)
	time.Sleep(600 * time.Millisecond)
	if b.State() != backend.Ready {
		t.Fatal("slept although auto-sleep is off")
	}
	// Persisted: a new store (Sleepy restart) reads it back.
	if reopened := settings.Open(&config.Config{}, path); reopened.AutoSleep() {
		t.Fatal("auto-sleep off not persisted")
	}
	store.SetAutoSleep(true)
	testutil.Until(t, 3*time.Second, func() bool { return b.State() == backend.Sleeping })
}

func TestConsoleRouter(t *testing.T) {
	b := newBackend(t)
	store, _ := newStore(t, time.Hour, 0)
	quits, reloads := 0, 0
	r := &console.Router{
		Backend:  b,
		Settings: store,
		Reload:   func() error { reloads++; return errors.New("boom") },
		Quit:     func() { quits++ },
		Status:   func() string { return "ok" },
	}

	r.HandleLine("start")
	if b.State() != backend.Starting {
		t.Fatal("start command ignored")
	}
	b.WaitReady(5*time.Second, nil)
	r.HandleLine("say hello") // forwarded to Java: no effect on Sleepy

	// sleepy commands work while Java runs and are never forwarded.
	r.HandleLine("sleepy off")
	if store.AutoSleep() {
		t.Fatal("sleepy off ignored")
	}
	r.HandleLine("  SLEEPY   on ")
	if !store.AutoSleep() {
		t.Fatal("sleepy on ignored")
	}
	r.HandleLine("sleepy reload")
	if reloads != 1 {
		t.Fatal("sleepy reload ignored")
	}
	r.HandleLine("sleepy wake") // already running: no-op
	r.HandleLine("sleepy sleep")
	if b.State() != backend.Stopping {
		t.Fatalf("state %s after sleepy sleep", b.State())
	}
	testutil.Until(t, 3*time.Second, func() bool { return b.State() == backend.Sleeping })

	r.HandleLine("sleepy wake")
	if b.State() != backend.Starting {
		t.Fatal("sleepy wake ignored")
	}
	b.WaitReady(5*time.Second, nil)
	r.HandleLine("stop") // while running: goes through Backend.Stop, does not quit Sleepy
	testutil.Until(t, 3*time.Second, func() bool { return b.State() == backend.Sleeping })
	if quits != 0 {
		t.Fatal("stop while running quit Sleepy")
	}
	r.HandleLine("stop") // asleep: quits Sleepy
	if quits != 1 {
		t.Fatal("stop while asleep did not quit")
	}
}
