package backend

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"sleepy/internal/config"
	"sleepy/internal/testutil"
)

func makeContainerBackend(t *testing.T, d *testutil.FakeDocker, container string, stopTimeout time.Duration) (*Backend, string) {
	t.Helper()
	containerPoll = 50 * time.Millisecond
	dir := testutil.TempServerDir(t, map[string]string{"server.properties": "server-port=25565\nmax-players=9\n"})
	b := New("test", config.Backend{Type: config.TypeDocker, Container: container, DockerEndpoint: d.Endpoint,
		Host: "127.0.0.1", Port: d.Port, Directory: dir, ProxyProtocol: "auto"}, stopTimeout)
	t.Cleanup(func() { <-b.Stop("test cleanup") })
	return b, dir
}

func TestContainerLifecycle(t *testing.T) {
	d := testutil.NewFakeDocker(t, map[string]string{"FAKE_NO_DONE": "1"})
	b, dir := makeContainerBackend(t, d, testutil.FakeContainer, 7*time.Second)
	rec := record(b)
	if !b.Start("test") || !d.Running() {
		t.Fatal("container not started")
	}
	if !b.WaitReady(5*time.Second, nil) {
		t.Fatal("not ready from the status ping")
	}
	if b.External() {
		t.Error("a start by Sleepy is reported as external")
	}
	<-b.Stop("test")
	if d.Running() || b.State() != Sleeping {
		t.Fatalf("container running %v, state %s", d.Running(), b.State())
	}
	testutil.Until(t, time.Second, func() bool { return len(rec.get()) == 4 })
	if got := rec.get(); !reflect.DeepEqual(got, []State{Starting, Ready, Stopping, Sleeping}) {
		t.Fatalf("states %v", got)
	}
	calls := strings.Join(d.Calls(), "\n")
	if !strings.Contains(calls, "POST /containers/mc/start") || !strings.Contains(calls, "POST /containers/mc/stop?t=7") {
		t.Errorf("calls:\n%s", calls)
	}
	// The server files belong to the container: never rewritten in docker mode.
	if props, _ := os.ReadFile(filepath.Join(dir, "server.properties")); string(props) != "server-port=25565\nmax-players=9\n" {
		t.Errorf("server.properties changed: %q", props)
	}
}

func TestContainerConsole(t *testing.T) {
	d := testutil.NewFakeDocker(t, nil)
	b, _ := makeContainerBackend(t, d, testutil.FakeContainer, 5*time.Second)
	b.Start("test")
	b.WaitReady(5*time.Second, nil)
	b.WriteConsole("say hello")
	testutil.Until(t, 3*time.Second, func() bool { return strings.Contains(d.Output(), "[fake] console: say hello") })
}

func TestContainerAdoptedWhenStartedOutside(t *testing.T) {
	d := testutil.NewFakeDocker(t, nil)
	b, _ := makeContainerBackend(t, d, testutil.FakeContainer, 5*time.Second)
	go b.WatchExternalStarts(50 * time.Millisecond)
	d.StartExternally()
	testutil.Until(t, 3*time.Second, func() bool { return b.State() == Ready })
	if !b.External() {
		t.Error("not reported as started outside Sleepy")
	}
	// Stopped outside Sleepy too: back to sleeping.
	d.StopExternally()
	testutil.Until(t, 3*time.Second, func() bool { return b.State() == Sleeping && !b.Running() })
	// And Sleepy can start it again itself.
	if !b.Start("test") || !b.WaitReady(5*time.Second, nil) || b.External() {
		t.Fatal("cannot start again after an external stop")
	}
}

func TestContainerAlreadyRunningAtLaunch(t *testing.T) {
	d := testutil.NewFakeDocker(t, nil)
	d.StartExternally()
	b, _ := makeContainerBackend(t, d, testutil.FakeContainer, 5*time.Second)
	go b.WatchExternalStarts(time.Hour) // the first check is immediate
	testutil.Until(t, 3*time.Second, func() bool { return b.State() == Ready })
}

func TestContainerUnknown(t *testing.T) {
	d := testutil.NewFakeDocker(t, nil)
	b, _ := makeContainerBackend(t, d, "nope", 5*time.Second)
	if b.Start("test") {
		t.Fatal("started an unknown container")
	}
	if b.State() != Sleeping {
		t.Fatalf("state %s", b.State())
	}
}

func TestContainerWithoutOpenStdin(t *testing.T) {
	d := testutil.NewFakeDocker(t, nil)
	d.SetOpenStdin(false)
	b, _ := makeContainerBackend(t, d, testutil.FakeContainer, 5*time.Second)
	b.Start("test")
	b.WaitReady(5*time.Second, nil)
	b.mu.Lock()
	inst := b.inst
	b.mu.Unlock()
	if err := inst.WriteConsole("say hi"); err == nil || !strings.Contains(err.Error(), "stdin_open") {
		t.Fatalf("error %v", err)
	}
}

func TestReadProxyProtocol(t *testing.T) {
	dir := testutil.TempServerDir(t, map[string]string{"config/paper-global.yml": "proxies:\n  proxy-protocol: true\n"})
	for mode, want := range map[string]bool{"auto": true, "on": true, "off": false} {
		if got, err := ReadProxyProtocol(dir, mode); got != want || err != nil {
			t.Errorf("%s: %v %v", mode, got, err)
		}
	}
	if got, _ := ReadProxyProtocol(t.TempDir(), "auto"); got {
		t.Error("auto without paper-global.yml")
	}
}
