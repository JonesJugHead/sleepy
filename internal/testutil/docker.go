package testutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// FakeDocker is a Docker Engine API serving one container, FakeContainer, whose process is the
// fake Minecraft server listening on 127.0.0.1:Port. Endpoint is what backend.docker_endpoint takes.
type FakeDocker struct {
	Endpoint string
	Port     int
	// Calls lists the API calls received, as "METHOD /path?query".
	mu        sync.Mutex
	calls     []string
	env       map[string]string
	dir       string
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	exited    chan struct{}
	running   bool
	startedAt string
	exitCode  int
	openStdin bool
	output    bytes.Buffer
}

const FakeContainer = "mc"

var versionPrefix = regexp.MustCompile(`^/v[0-9.]+`)

func NewFakeDocker(t *testing.T, env map[string]string) *FakeDocker {
	t.Helper()
	d := &FakeDocker{Port: FreePort(t), env: env, openStdin: true}
	d.dir = TempServerDir(t, map[string]string{
		"server.properties": fmt.Sprintf("server-ip=127.0.0.1\nserver-port=%d\n", d.Port),
	})
	srv := httptest.NewServer(http.HandlerFunc(d.serve))
	d.Endpoint = "tcp://" + strings.TrimPrefix(srv.URL, "http://")
	t.Cleanup(func() {
		d.kill()
		srv.Close()
	})
	return d
}

// SetOpenStdin sets whether the container was created with stdin open.
func (d *FakeDocker) SetOpenStdin(open bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.openStdin = open
}

func (d *FakeDocker) Calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

// Output is what the container printed so far.
func (d *FakeDocker) Output() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.output.String()
}

func (d *FakeDocker) Running() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.running
}

// StartExternally starts the container as a panel or `docker start` would.
func (d *FakeDocker) StartExternally() { d.start() }

// StopExternally stops the container as `docker stop` would, without Sleepy.
func (d *FakeDocker) StopExternally() { d.stop(10 * time.Second) }

func (d *FakeDocker) serve(w http.ResponseWriter, r *http.Request) {
	path := versionPrefix.ReplaceAllString(r.URL.Path, "")
	d.mu.Lock()
	d.calls = append(d.calls, strings.TrimSuffix(r.Method+" "+path+"?"+r.URL.RawQuery, "?"))
	d.mu.Unlock()
	name, action, ok := strings.Cut(strings.TrimPrefix(path, "/containers/"), "/")
	if !ok || !strings.HasPrefix(path, "/containers/") {
		http.Error(w, `{"message":"page not found"}`, http.StatusNotFound)
		return
	}
	if name != FakeContainer {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"message":"No such container: %s"}`, name)
		return
	}
	switch {
	case action == "json" && r.Method == http.MethodGet:
		d.mu.Lock()
		body := map[string]any{
			"State":  map[string]any{"Running": d.running, "StartedAt": d.startedAt, "ExitCode": d.exitCode},
			"Config": map[string]any{"OpenStdin": d.openStdin},
		}
		d.mu.Unlock()
		json.NewEncoder(w).Encode(body)
	case action == "start" && r.Method == http.MethodPost:
		if !d.start() {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case action == "stop" && r.Method == http.MethodPost:
		t, _ := strconv.Atoi(r.URL.Query().Get("t"))
		if !d.stop(time.Duration(t) * time.Second) {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case action == "attach" && r.Method == http.MethodPost:
		d.attach(w)
	default:
		http.Error(w, `{"message":"not implemented by the fake"}`, http.StatusNotImplemented)
	}
}

func (d *FakeDocker) start() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.running {
		return false
	}
	cmd := exec.Command("sh", "-c", FakeServerCommand(d.env))
	cmd.Dir = d.dir
	stdin, _ := cmd.StdinPipe()
	cmd.Stdout = &lockedWriter{d: d}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		panic(err)
	}
	exited := make(chan struct{})
	d.cmd, d.stdin, d.exited, d.running = cmd, stdin, exited, true
	d.startedAt = time.Now().Format(time.RFC3339Nano)
	go func() {
		cmd.Wait()
		d.mu.Lock()
		d.running, d.exitCode = false, cmd.ProcessState.ExitCode()
		d.mu.Unlock()
		close(exited)
	}()
	return true
}

func (d *FakeDocker) stop(timeout time.Duration) bool {
	d.mu.Lock()
	if !d.running {
		d.mu.Unlock()
		return false
	}
	stdin, exited, cmd := d.stdin, d.exited, d.cmd
	d.mu.Unlock()
	io.WriteString(stdin, "stop\n")
	select {
	case <-exited:
	case <-time.After(timeout):
		cmd.Process.Kill()
		<-exited
	}
	return true
}

func (d *FakeDocker) kill() {
	d.mu.Lock()
	cmd, exited := d.cmd, d.exited
	d.mu.Unlock()
	if cmd != nil {
		cmd.Process.Kill()
		<-exited
	}
}

func (d *FakeDocker) attach(w http.ResponseWriter) {
	conn, rw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	io.WriteString(conn, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
	d.mu.Lock()
	stdin := d.stdin
	d.mu.Unlock()
	if stdin != nil {
		io.Copy(stdin, rw.Reader)
	}
}

type lockedWriter struct{ d *FakeDocker }

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.d.mu.Lock()
	defer l.d.mu.Unlock()
	return l.d.output.Write(p)
}

// WriteFileAt writes a file with a given modification time (stale locks).
func WriteFileAt(t *testing.T, path, content string, mtime time.Time) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}
