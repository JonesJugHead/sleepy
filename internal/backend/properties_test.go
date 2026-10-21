package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var testValues = [][2]string{{"server-port", "25566"}, {"server-ip", "127.0.0.1"}}

func TestSetProperties(t *testing.T) {
	cases := []struct{ in, want string }{
		{
			"#Minecraft server properties\nmotd=A Minecraft Server\nserver-ip=\nserver-port=25565\nmax-players=10\n",
			"#Minecraft server properties\nmotd=A Minecraft Server\nserver-ip=127.0.0.1\nserver-port=25566\nmax-players=10\n",
		},
		{"motd=hi\r\n", "motd=hi\r\nserver-port=25566\r\nserver-ip=127.0.0.1\r\n"},
		{"", "server-port=25566\nserver-ip=127.0.0.1\n"},
		{"server-port = 1\n#server-ip=x\nserver-port:2\nserver-ip=", "server-port=25566\n#server-ip=x\nserver-port=25566\nserver-ip=127.0.0.1"},
	}
	for _, c := range cases {
		if got := SetProperties(c.in, testValues); got != c.want {
			t.Errorf("SetProperties(%q)\n got %q\nwant %q", c.in, got, c.want)
		}
	}
}

func TestGetProperty(t *testing.T) {
	if v, ok := GetProperty("a=1\nmax-players = 5\nmax-players=8\n", "max-players"); !ok || v != "8" {
		t.Errorf("got %q %v", v, ok)
	}
	if _, ok := GetProperty("a=1", "max-players"); ok {
		t.Error("found a missing key")
	}
}

func TestRewriteServerPropertiesOnDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.properties")
	os.WriteFile(path, []byte("max-players=42\nserver-port=25565\n"), 0o644)
	if err := RewriteServerProperties(dir, 30001); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); string(got) != "max-players=42\nserver-port=30001\nserver-ip=127.0.0.1\n" {
		t.Errorf("got %q", got)
	}
	if n := ReadMaxPlayers(dir); n != 42 {
		t.Errorf("max players %d", n)
	}
	empty := t.TempDir()
	if n := ReadMaxPlayers(empty); n != 20 {
		t.Errorf("default max players %d", n)
	}
	RewriteServerProperties(empty, 30002)
	if got, _ := os.ReadFile(filepath.Join(empty, "server.properties")); string(got) != "server-port=30002\nserver-ip=127.0.0.1\n" {
		t.Errorf("created file %q", got)
	}
}

func TestReadMotd(t *testing.T) {
	dir := t.TempDir()
	// As written in server.properties: \u00A7 escapes, \n, an escaped backslash, a truncated \u.
	line := strings.ReplaceAll("motd=%u00A7aHello%n%u00A7fworld %%o/ %u00e9 %u12", "%", string(rune(92)))
	os.WriteFile(filepath.Join(dir, "server.properties"), []byte(line+"\n"), 0o644)
	if got, want := ReadMotd(dir), "§aHello\n§fworld "+string(rune(92))+"o/ é u12"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := ReadMotd(t.TempDir()); got != "" {
		t.Errorf("missing file: %q", got)
	}
}
