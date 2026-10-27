package proxy

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sleepy/internal/backend"
	"sleepy/internal/protocol"
	"sleepy/internal/testutil"
)

// joinAnswering logs in like a 1.13+ client: it answers every login plugin request with "not
// understood", and returns the final login disconnect text and how many requests it answered.
func joinAnswering(t *testing.T, port int, protocolVersion int32, name string) (string, int) {
	t.Helper()
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(20 * time.Second))
	c.Write(concat(handshake(port, 2, protocolVersion), protocol.EncodeLoginStart(name)))
	r := protocol.NewReader(1 << 20)
	buf := make([]byte, 4096)
	answered := 0
	for {
		p, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		if p == nil {
			n, err := c.Read(buf)
			if err != nil {
				t.Fatalf("connection ended without a disconnect (%v), %d requests answered", err, answered)
			}
			r.Push(buf[:n])
			continue
		}
		switch p.ID {
		case protocol.LoginPluginRequestID:
			cur := protocol.NewCursor(p.Data)
			id, _ := cur.ReadVarInt()
			channel, _ := cur.ReadString(256)
			if channel != keepAliveChannel {
				t.Fatalf("channel %q", channel)
			}
			c.Write(protocol.EncodeLoginPluginResponse(id))
			answered++
		case 0x00:
			return str(t, p), answered
		default:
			t.Fatalf("unexpected packet 0x%x", p.ID)
		}
	}
}

func shortKeepAlive(t *testing.T) {
	old := keepAliveInterval
	keepAliveInterval = 200 * time.Millisecond
	t.Cleanup(func() { keepAliveInterval = old })
}

func TestKeepAliveWhileWaiting(t *testing.T) {
	shortKeepAlive(t)
	s := newSetup(t, map[string]string{"FAKE_DELAY": "1500", "FAKE_NO_DONE": "1"}, "")
	text, answered := joinAnswering(t, s.port, 767, "Steve")
	// The fake server rejects anything after the login start: the answers were not replayed.
	if text != `{"text":"hello Steve via 127.0.0.1"}` {
		t.Fatalf("reply %s", text)
	}
	if answered < 3 {
		t.Fatalf("only %d keep-alive requests over a 1.5 s start", answered)
	}
}

func TestHoldBeyondClientTimeoutNeedsKeepAlive(t *testing.T) {
	shortKeepAlive(t)
	// hold_timeout is honoured for 1.13+ clients...
	s := newSetup(t, map[string]string{"FAKE_DELAY": "1000", "FAKE_NO_DONE": "1"}, "hold_timeout = 5")
	if text, _ := joinAnswering(t, s.port, 393, "Steve"); !strings.Contains(text, "hello Steve") {
		t.Fatalf("1.13 client: %s", text)
	}
	// ...while older clients get no plugin request (they do not know it).
	s = newSetup(t, map[string]string{"FAKE_DELAY": "1000", "FAKE_NO_DONE": "1"}, "hold_timeout = 5")
	if text, answered := joinAnswering(t, s.port, 340, "Alex"); answered != 0 || !strings.Contains(text, "hello Alex") {
		t.Fatalf("1.12 client: %s, %d requests", text, answered)
	}
}

func TestClientNotAnsweringIsKicked(t *testing.T) {
	shortKeepAlive(t)
	s := newSetup(t, map[string]string{"FAKE_DELAY": "800", "FAKE_NO_DONE": "1"}, "kick_message = \"Retry\"")
	// exchange never answers the plugin requests.
	ps := packets(t, exchange(t, s.port, handshake(s.port, 2, 767), protocol.EncodeLoginStart("Mute")))
	last := ps[len(ps)-1]
	if last.ID != 0x00 || str(t, last) != `{"text":"Retry"}` {
		t.Fatalf("last packet 0x%x", last.ID)
	}
}

func newPowerSetup(t *testing.T, files map[string]string) (*setup, string) {
	t.Helper()
	powerDir := t.TempDir()
	for name, content := range files {
		os.WriteFile(filepath.Join(powerDir, name), []byte(content), 0o644)
	}
	toml := fmt.Sprintf("[power]\ndirectory = %q\nstopped_message = \"Admin says no\"\nbackup_message = \"Backup running\"\nrestore_message = \"Restoring\"\n[motd]\nstopped = \"Closed\"", powerDir)
	return newSetup(t, nil, toml), powerDir
}

func TestAdminStoppedNeverWakes(t *testing.T) {
	s, powerDir := newPowerSetup(t, map[string]string{"admin.json": `{"mode": "stopped", "by": "alice"}`})
	ps := packets(t, exchange(t, s.port, handshake(s.port, 2, 767), protocol.EncodeLoginStart("Steve")))
	if len(ps) != 1 || str(t, ps[0]) != `{"text":"Admin says no"}` || s.backend.State() != backend.Sleeping {
		t.Fatalf("reply %v, state %s", ps, s.backend.State())
	}
	st := packets(t, exchange(t, s.port, handshake(s.port, 1, 767), protocol.EncodeStatusRequest(), ping))
	var js struct{ Description struct{ Text string } }
	json.Unmarshal([]byte(str(t, st[0])), &js)
	if js.Description.Text != "Closed" {
		t.Fatalf("MOTD %q", js.Description.Text)
	}
	// Back to auto: the next player wakes it.
	os.WriteFile(filepath.Join(powerDir, "admin.json"), []byte(`{"mode": "auto"}`), 0o644)
	ps = packets(t, exchange(t, s.port, handshake(s.port, 2, 767), protocol.EncodeLoginStart("Steve")))
	if len(ps) == 0 || !strings.Contains(str(t, ps[len(ps)-1]), "hello Steve") {
		t.Fatalf("auto mode: %v", ps)
	}
}

func TestLocksBlockWake(t *testing.T) {
	s, powerDir := newPowerSetup(t, map[string]string{"backup.lock": `{"pid": 1}`})
	ps := packets(t, exchange(t, s.port, handshake(s.port, 2, 767), protocol.EncodeLoginStart("Steve")))
	if len(ps) != 1 || str(t, ps[0]) != `{"text":"Backup running"}` {
		t.Fatalf("backup: %v", ps)
	}
	os.WriteFile(filepath.Join(powerDir, "restore.lock"), []byte(`{}`), 0o644)
	ps = packets(t, exchange(t, s.port, handshake(s.port, 2, 767), protocol.EncodeLoginStart("Steve")))
	if len(ps) != 1 || str(t, ps[0]) != `{"text":"Restoring"}` {
		t.Fatalf("restore: %v", ps)
	}
	if s.backend.State() != backend.Sleeping {
		t.Fatal("woken despite the locks")
	}
	// A stale lock (crashed script) is ignored.
	old := time.Now().Add(-7 * time.Hour)
	os.Remove(filepath.Join(powerDir, "restore.lock"))
	os.Chtimes(filepath.Join(powerDir, "backup.lock"), old, old)
	ps = packets(t, exchange(t, s.port, handshake(s.port, 2, 767), protocol.EncodeLoginStart("Steve")))
	if len(ps) == 0 || !strings.Contains(str(t, ps[len(ps)-1]), "hello Steve") {
		t.Fatalf("stale lock: %v", ps)
	}
}

func TestDockerBackendEndToEnd(t *testing.T) {
	d := testutil.NewFakeDocker(t, map[string]string{"FAKE_NO_DONE": "1"})
	port := testutil.FreePort(t)
	// No server files: docker mode works without access to them.
	empty := t.TempDir()
	text := fmt.Sprintf("public_address = \"127.0.0.1:%d\"\n[backend]\ntype = \"docker\"\ncontainer = %q\ndocker_endpoint = %q\naddress = \"127.0.0.1:%d\"\ndirectory = %q\n",
		port, testutil.FakeContainer, d.Endpoint, d.Port, empty)
	s := serve(t, text, port, empty)

	ps := packets(t, exchange(t, s.port, handshake(s.port, 2, 767), protocol.EncodeLoginStart("Steve")))
	if len(ps) == 0 || str(t, ps[len(ps)-1]) != `{"text":"hello Steve via 127.0.0.1"}` {
		t.Fatalf("reply %v", ps)
	}
	if !d.Running() {
		t.Fatal("container not running")
	}
	// The real status (max players 20 from the fake) is remembered for when it sleeps.
	testutil.Until(t, 8*time.Second, func() bool { return s.backend.RememberedStatus().MaxPlayers == 20 })
	<-s.backend.Stop("test")
	if d.Running() {
		t.Fatal("container still running")
	}
	st := packets(t, exchange(t, s.port, handshake(s.port, 1, 767), protocol.EncodeStatusRequest(), ping))
	var js struct{ Players struct{ Max int } }
	json.Unmarshal([]byte(str(t, st[0])), &js)
	if js.Players.Max != 20 {
		t.Fatalf("max players while asleep: %d", js.Players.Max)
	}
}
