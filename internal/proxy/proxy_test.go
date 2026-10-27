package proxy

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"sleepy/internal/backend"
	"sleepy/internal/config"
	"sleepy/internal/power"
	"sleepy/internal/protocol"
	"sleepy/internal/settings"
	"sleepy/internal/testutil"
)

func TestMain(m *testing.M) {
	testutil.MaybeRunFakeServer()
	os.Exit(m.Run())
}

type setup struct {
	backend *backend.Backend
	port    int
	dir     string
}

func newSetup(t *testing.T, env map[string]string, toml string) *setup {
	t.Helper()
	return newSetupFull(t, env, toml, "", nil)
}

// newSetupFull also takes extra [backend] settings and extra files for the server directory.
func newSetupFull(t *testing.T, env map[string]string, toml, backendTOML string, files map[string]string) *setup {
	t.Helper()
	port := testutil.FreePort(t)
	all := map[string]string{"server.properties": "max-players=7\n"}
	for name, content := range files {
		all[name] = content
	}
	dir := testutil.TempServerDir(t, all)
	text := fmt.Sprintf("public_address = \"127.0.0.1:%d\"\n%s\n[backend]\ncommand = %q\naddress = \"127.0.0.1:%d\"\ndirectory = %q\n%s\n",
		port, toml, testutil.FakeServerCommand(env), testutil.FreePort(t), dir, backendTOML)
	return serve(t, text, port, dir)
}

// serve runs a proxy for the config text on port.
func serve(t *testing.T, text string, port int, dir string) *setup {
	t.Helper()
	cfg, err := config.Parse(text, "")
	if err != nil {
		t.Fatal(err)
	}
	b := backend.New("test", cfg.Backend, 5*time.Second)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	store := settings.Open(cfg, filepath.Join(t.TempDir(), "state.json"))
	go (&Proxy{Settings: store, Power: power.Open(cfg.Power.Directory), ResolveBackend: func(*protocol.Handshake) *backend.Backend { return b }}).Serve(ln)
	t.Cleanup(func() {
		ln.Close()
		<-b.Stop("test cleanup")
	})
	return &setup{backend: b, port: port, dir: dir}
}

// exchange sends bytes and collects the reply until the server closes the connection.
func exchange(t *testing.T, port int, msgs ...[]byte) []byte {
	t.Helper()
	c, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(15 * time.Second))
	c.Write(concat(msgs...))
	reply, _ := io.ReadAll(c)
	return reply
}

func packets(t *testing.T, b []byte) []*protocol.RawPacket {
	t.Helper()
	r := protocol.NewReader(1 << 20)
	r.Push(b)
	var out []*protocol.RawPacket
	for {
		p, err := r.Next()
		if err != nil {
			t.Fatal(err)
		}
		if p == nil {
			return out
		}
		out = append(out, p)
	}
}

func str(t *testing.T, p *protocol.RawPacket) string {
	t.Helper()
	s, err := protocol.NewCursor(p.Data).ReadString(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func handshake(port, intent int, protocolVersion int32) []byte {
	return protocol.EncodeHandshake(protocol.Handshake{ProtocolVersion: protocolVersion, Address: "localhost", Port: uint16(port), Intent: int32(intent)})
}

var ping = protocol.EncodePacket(protocol.PingID, []byte{0, 0, 0, 0, 0, 0xab, 0xcd, 0xef})

func legacyText(b []byte) string {
	var units []uint16
	for i := 3; i+1 < len(b); i += 2 {
		units = append(units, binary.BigEndian.Uint16(b[i:]))
	}
	return string(utf16.Decode(units))
}

func TestStatusWhileAsleep(t *testing.T) {
	s := newSetup(t, nil, "")
	os.WriteFile(filepath.Join(s.dir, "server-icon.png"), []byte{0x89, 0x50, 0x4e, 0x47}, 0o644)
	ps := packets(t, exchange(t, s.port, handshake(s.port, 1, 5), protocol.EncodeStatusRequest(), ping))
	if len(ps) != 2 {
		t.Fatalf("got %d packets", len(ps))
	}
	var js struct {
		Version struct {
			Name     string
			Protocol int
		}
		Players     struct{ Max, Online int }
		Description struct{ Text string }
		Favicon     string
	}
	json.Unmarshal([]byte(str(t, ps[0])), &js)
	if js.Version.Name != "Sleepy" || js.Version.Protocol != 5 || js.Players.Max != 7 || js.Players.Online != 0 ||
		!strings.Contains(js.Description.Text, "asleep") || js.Favicon != "data:image/png;base64,iVBORw==" {
		t.Errorf("status %+v", js)
	}
	if ps[1].ID != protocol.PingID || !bytes.Equal(ps[1].Data, ping[2:]) {
		t.Error("pong does not echo the ping")
	}
	if s.backend.State() != backend.Sleeping {
		t.Error("a status ping woke the server")
	}
}

func TestLegacyPings(t *testing.T) {
	s := newSetup(t, nil, "")
	utf16be := func(v string) []byte {
		var b []byte
		for _, u := range utf16.Encode([]rune(v)) {
			b = binary.BigEndian.AppendUint16(b, u)
		}
		return b
	}
	req16 := concat([]byte{0xfe, 0x01, 0xfa, 0x00, 0x0b}, utf16be("MC|PingHost"), []byte{0x00, 0x07, 78, 0, 0, 0, 0, 0, 0})
	if got := strings.Split(legacyText(exchange(t, s.port, req16)), "\x00")[:3]; !reflect.DeepEqual(got, []string{"§1", "78", "Sleepy"}) {
		t.Errorf("1.6 ping: %q", got)
	}
	if got := legacyText(exchange(t, s.port, []byte{0xfe})); !strings.HasSuffix(got, "§0§7") {
		t.Errorf("pre-1.4 ping: %q", got)
	}
	if s.backend.State() != backend.Sleeping {
		t.Error("a legacy ping woke the server")
	}
}

func TestLoginWakesAndRelays(t *testing.T) {
	s := newSetup(t, nil, "")
	ps := packets(t, exchange(t, s.port, handshake(s.port, 2, 767), protocol.EncodeLoginStart("Steve")))
	// The fake server answers "hello <name> via <bind ip>", proving it received the replayed packets.
	if len(ps) != 1 || str(t, ps[0]) != `{"text":"hello Steve via 127.0.0.1"}` {
		t.Fatalf("reply %v", ps)
	}
	if s.backend.State() != backend.Ready {
		t.Error("server not ready")
	}
	testutil.Until(t, 2*time.Second, func() bool { return s.backend.ActiveConnections() == 0 })
}

func TestStatusRelayedWhenReady(t *testing.T) {
	s := newSetup(t, nil, "")
	s.backend.Start("test")
	s.backend.WaitReady(5*time.Second, nil)
	ps := packets(t, exchange(t, s.port, handshake(s.port, 1, 767), protocol.EncodeStatusRequest(), ping))
	if len(ps) == 0 || !strings.Contains(str(t, ps[0]), `"name":"Fake"`) {
		t.Fatalf("status not relayed: %v", ps)
	}
}

func TestHoldTimeoutKicks(t *testing.T) {
	s := newSetup(t, map[string]string{"FAKE_DELAY": "3000"}, "hold_timeout = 0.5\nkick_message = \"Reviens vite\"")
	ps := packets(t, exchange(t, s.port, handshake(s.port, 2, 767), protocol.EncodeLoginStart("Alex")))
	if len(ps) != 1 || str(t, ps[0]) != `{"text":"Reviens vite"}` {
		t.Fatalf("reply %v", ps)
	}
	if s.backend.State() != backend.Starting {
		t.Errorf("state %s", s.backend.State())
	}
}

func TestWhitelistOnly(t *testing.T) {
	s := newSetup(t, nil, "whitelist_only = true\nkick_message = \"Nope\"")
	os.WriteFile(filepath.Join(s.dir, "whitelist.json"), []byte(`[{"uuid":"x","name":"Notch"}]`), 0o644)
	ps := packets(t, exchange(t, s.port, handshake(s.port, 2, 767), protocol.EncodeLoginStart("Griefer")))
	if len(ps) != 1 || str(t, ps[0]) != `{"text":"Nope"}` || s.backend.State() != backend.Sleeping {
		t.Fatalf("unlisted player: %v, state %s", ps, s.backend.State())
	}
	ps = packets(t, exchange(t, s.port, handshake(s.port, 2, 767), protocol.EncodeLoginStart("notch")))
	if len(ps) != 1 || !strings.Contains(str(t, ps[0]), "hello notch") {
		t.Fatalf("listed player: %v", ps)
	}
}

func TestTransferIntent(t *testing.T) {
	s := newSetup(t, nil, "")
	ps := packets(t, exchange(t, s.port, handshake(s.port, 3, 767), protocol.EncodeLoginStart("Steve")))
	if len(ps) != 1 || !strings.Contains(str(t, ps[0]), "hello Steve") {
		t.Fatalf("reply %v", ps)
	}
}

func TestPlayerLeavingWhileHeld(t *testing.T) {
	s := newSetup(t, map[string]string{"FAKE_DELAY": "1500"}, "")
	c, _ := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
	c.Write(concat(handshake(s.port, 2, 767), protocol.EncodeLoginStart("Quitter")))
	testutil.Until(t, 2*time.Second, func() bool { return s.backend.State() == backend.Starting })
	c.Close()
	// The server still comes up (the idle watcher will put it back to sleep), and nobody is counted.
	if !s.backend.WaitReady(5*time.Second, nil) {
		t.Fatal("not ready")
	}
	time.Sleep(200 * time.Millisecond)
	if n := s.backend.ActiveConnections(); n != 0 {
		t.Fatalf("%d connections counted", n)
	}
}

func TestGarbage(t *testing.T) {
	s := newSetup(t, nil, "")
	if r := exchange(t, s.port, []byte{0x00, 0x01}); len(r) != 0 {
		t.Error("zero-length packet got a reply")
	}
	if r := exchange(t, s.port, protocol.EncodePacket(0x00, []byte{0x05})); len(r) != 0 {
		t.Error("truncated handshake got a reply")
	}
	if s.backend.State() != backend.Sleeping {
		t.Error("garbage woke the server")
	}
	ps := packets(t, exchange(t, s.port, handshake(s.port, 1, 767), protocol.EncodeStatusRequest(), ping))
	if len(ps) != 2 {
		t.Fatal("not serving after garbage")
	}
}

const paperGlobal = "proxies:\n  bungee-cord:\n    online-mode: true\n  proxy-protocol: %v\n  velocity:\n    enabled: false\n"

func TestProxyProtocolAutoOnPaper(t *testing.T) {
	// FAKE_NO_DONE: readiness comes from Sleepy's own status ping, which must carry the header too.
	s := newSetupFull(t, map[string]string{"FAKE_NO_DONE": "1"}, "", "", map[string]string{
		"config/paper-global.yml": fmt.Sprintf(paperGlobal, false),
	})
	ps := packets(t, exchange(t, s.port, handshake(s.port, 2, 767), protocol.EncodeLoginStart("Steve")))
	// The fake server (like Paper) rejects connections without the header, and reports its source.
	if len(ps) != 1 || str(t, ps[0]) != `{"text":"hello Steve via 127.0.0.1 from 127.0.0.1"}` {
		t.Fatalf("reply %v", ps)
	}
	if got, _ := os.ReadFile(filepath.Join(s.dir, "config/paper-global.yml")); string(got) != fmt.Sprintf(paperGlobal, true) {
		t.Errorf("paper-global.yml not updated:\n%s", got)
	}
	if !s.backend.ProxyProtocol() {
		t.Error("backend does not report the PROXY protocol")
	}
	st := packets(t, exchange(t, s.port, handshake(s.port, 1, 767), protocol.EncodeStatusRequest(), ping))
	if len(st) != 2 || !strings.Contains(str(t, st[0]), `"name":"Fake"`) {
		t.Fatalf("relayed status failed with the PROXY protocol: %v", st)
	}
}

func TestProxyProtocolOffDisablesItOnPaper(t *testing.T) {
	s := newSetupFull(t, nil, "", `proxy_protocol = "off"`, map[string]string{
		"config/paper-global.yml": fmt.Sprintf(paperGlobal, true),
	})
	ps := packets(t, exchange(t, s.port, handshake(s.port, 2, 767), protocol.EncodeLoginStart("Steve")))
	if len(ps) != 1 || str(t, ps[0]) != `{"text":"hello Steve via 127.0.0.1"}` {
		t.Fatalf("reply %v", ps)
	}
	if got, _ := os.ReadFile(filepath.Join(s.dir, "config/paper-global.yml")); string(got) != fmt.Sprintf(paperGlobal, false) {
		t.Errorf("paper-global.yml not updated:\n%s", got)
	}
}

func TestMotdSourceServer(t *testing.T) {
	s := newSetupFull(t, nil, "[motd]\nsource = \"server\"", "", map[string]string{
		"server.properties": "max-players=7\nmotd=\u00A7bFrom properties\n",
	})
	description := func() string {
		ps := packets(t, exchange(t, s.port, handshake(s.port, 1, 767), protocol.EncodeStatusRequest(), ping))
		var js struct{ Description json.RawMessage }
		json.Unmarshal([]byte(str(t, ps[0])), &js)
		return string(js.Description)
	}

	// Never ran yet: motd= from server.properties.
	if got := description(); got != `{"text":"§bFrom properties"}` {
		t.Fatalf("before the first run: %s", got)
	}
	// Once the server has run, its real MOTD (the fake server's) is remembered and shown asleep.
	s.backend.Start("test")
	s.backend.WaitReady(5*time.Second, nil)
	testutil.Until(t, 3*time.Second, func() bool { return len(s.backend.RememberedStatus().Description) > 0 })
	<-s.backend.Stop("test")
	if got := description(); got != `{"text":"fake"}` {
		t.Fatalf("after a run: %s", got)
	}
	if got := legacyText(exchange(t, s.port, []byte{0xfe, 0x01})); !strings.Contains(got, "\x00fake\x00") {
		t.Fatalf("legacy ping: %q", got)
	}
}
