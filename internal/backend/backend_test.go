package backend

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"sleepy/internal/config"
	"sleepy/internal/testutil"
)

func TestMain(m *testing.M) {
	testutil.MaybeRunFakeServer()
	os.Exit(m.Run())
}

// stateRecorder collects every state the backend goes through.
type stateRecorder struct {
	mu     sync.Mutex
	states []State
}

func (r *stateRecorder) get() []State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]State(nil), r.states...)
}

func record(b *Backend) *stateRecorder {
	r := &stateRecorder{}
	b.observer = func(s State) {
		r.mu.Lock()
		r.states = append(r.states, s)
		r.mu.Unlock()
	}
	return r
}

func makeBackend(t *testing.T, env map[string]string, stopTimeout time.Duration) (*Backend, string) {
	t.Helper()
	dir := testutil.TempServerDir(t, map[string]string{"server.properties": "server-port=25565\n"})
	b := New("test", config.Backend{Command: testutil.FakeServerCommand(env), Port: testutil.FreePort(t), Directory: dir}, stopTimeout)
	t.Cleanup(func() { <-b.Stop("test cleanup") })
	return b, dir
}

func TestLifecycle(t *testing.T) {
	b, dir := makeBackend(t, nil, 5*time.Second)
	rec := record(b)
	if !b.Start("test") || b.State() != Starting {
		t.Fatal("not starting")
	}
	props, _ := os.ReadFile(filepath.Join(dir, "server.properties"))
	if !strings.Contains(string(props), "server-port="+strconv.Itoa(b.Port())+"\nserver-ip=127.0.0.1") {
		t.Fatalf("server.properties not rewritten: %q", props)
	}
	if !b.WaitReady(5*time.Second, nil) {
		t.Fatal("not ready")
	}
	<-b.Stop("test")
	testutil.Until(t, time.Second, func() bool { return len(rec.get()) == 4 })
	if got := rec.get(); !reflect.DeepEqual(got, []State{Starting, Ready, Stopping, Sleeping}) {
		t.Fatalf("states %v", got)
	}
	if b.Running() {
		t.Fatal("still running")
	}
}

func TestReadyFromStatusPing(t *testing.T) {
	b, _ := makeBackend(t, map[string]string{"FAKE_NO_DONE": "1"}, 5*time.Second)
	b.Start("test")
	if !b.WaitReady(5*time.Second, nil) {
		t.Fatal("not ready from the status ping")
	}
}

func TestUnexpectedExit(t *testing.T) {
	b, _ := makeBackend(t, map[string]string{"FAKE_CRASH_AFTER": "50", "FAKE_DELAY": "5000"}, 5*time.Second)
	b.Start("test")
	start := time.Now()
	if b.WaitReady(5*time.Second, nil) {
		t.Fatal("ready despite the crash")
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("the crash did not end the wait early")
	}
	testutil.Until(t, time.Second, func() bool { return b.State() == Sleeping && !b.Running() })
}

func TestStopEscalatesToSIGTERM(t *testing.T) {
	b, _ := makeBackend(t, map[string]string{"FAKE_IGNORE_STOP": "1"}, 300*time.Millisecond)
	b.Start("test")
	b.WaitReady(5*time.Second, nil)
	start := time.Now()
	<-b.Stop("test")
	if time.Since(start) < 250*time.Millisecond || b.State() != Sleeping {
		t.Fatalf("stopped after %v in state %s", time.Since(start), b.State())
	}
}

func TestStartWhileStoppingRestarts(t *testing.T) {
	b, _ := makeBackend(t, nil, 5*time.Second)
	rec := record(b)
	b.Start("test")
	b.WaitReady(5*time.Second, nil)
	stopped := b.Stop("test")
	if !b.Start("player") {
		t.Fatal("start refused while stopping")
	}
	<-stopped
	if !b.WaitReady(5*time.Second, nil) {
		t.Fatal("did not restart")
	}
	want := []State{Starting, Ready, Stopping, Sleeping, Starting, Ready}
	testutil.Until(t, time.Second, func() bool { return len(rec.get()) >= len(want) })
	if got := rec.get(); !reflect.DeepEqual(got, want) {
		t.Fatalf("states %v", got)
	}
}

func TestCommandWithEnvAssignment(t *testing.T) {
	// VAR=value prefixes must work (they would not with an `exec` prefix).
	b, _ := makeBackend(t, map[string]string{"FAKE_DELAY": "100"}, 5*time.Second)
	b.Start("test")
	if !b.WaitReady(5*time.Second, nil) {
		t.Fatal("not ready")
	}
}

func TestConnectionCounter(t *testing.T) {
	b := New("test", config.Backend{}, time.Second)
	got := []int{b.Acquire(), b.Acquire(), b.Release(), b.Release(), b.Release()}
	if !reflect.DeepEqual(got, []int{1, 2, 1, 0, 0}) {
		t.Fatalf("counts %v", got)
	}
}
