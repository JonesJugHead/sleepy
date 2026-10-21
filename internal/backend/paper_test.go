package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Excerpt of a real paper-global.yml.
const paperGlobal = `# This is the global configuration file for Paper.
_version: 31
messages:
  kick:
    connection-throttle: Connection throttled! Please wait before reconnecting.
proxies:
  bungee-cord:
    online-mode: true
  proxy-protocol: false
  velocity:
    enabled: false
    online-mode: true
    secret: ''
scoreboards:
  save-empty-scoreboard-teams: true
`

func TestSetPaperProxyProtocol(t *testing.T) {
	cases := []struct {
		name, in string
		value    bool
		want     string
	}{
		{"flip in place", paperGlobal, true, strings.Replace(paperGlobal, "  proxy-protocol: false", "  proxy-protocol: true", 1)},
		{"already set", paperGlobal, false, paperGlobal},
		{"nested key with the same name is not the target",
			"proxies:\n  velocity:\n    proxy-protocol: false\nother: 1\n", true,
			"proxies:\n  proxy-protocol: true\n  velocity:\n    proxy-protocol: false\nother: 1\n"},
		{"no proxies block", "a: 1\n", true, "a: 1\nproxies:\n  proxy-protocol: true\n"},
		{"CRLF and 4-space indent", "proxies:\r\n    proxy-protocol: true\r\n", false, "proxies:\r\n    proxy-protocol: false\r\n"},
	}
	for _, c := range cases {
		if got := setPaperProxyProtocol(c.in, c.value); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestConfigureProxyProtocol(t *testing.T) {
	notPaper := t.TempDir()
	for mode, want := range map[string]bool{"auto": false, "on": true, "off": false} {
		if active, changed, err := ConfigureProxyProtocol(notPaper, mode); active != want || changed || err != nil {
			t.Errorf("not Paper, %s: active=%v changed=%v err=%v", mode, active, changed, err)
		}
	}

	paper := t.TempDir()
	path := filepath.Join(paper, "config", "paper-global.yml")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte(paperGlobal), 0o644)
	if active, changed, _ := ConfigureProxyProtocol(paper, "auto"); !active || !changed {
		t.Errorf("auto on Paper: active=%v changed=%v", active, changed)
	}
	if active, changed, _ := ConfigureProxyProtocol(paper, "auto"); !active || changed {
		t.Errorf("second auto: active=%v changed=%v", active, changed)
	}
	if active, changed, _ := ConfigureProxyProtocol(paper, "off"); active || !changed {
		t.Errorf("off on Paper: active=%v changed=%v", active, changed)
	}
	if got, _ := os.ReadFile(path); string(got) != paperGlobal {
		t.Errorf("file not restored:\n%s", got)
	}
}
