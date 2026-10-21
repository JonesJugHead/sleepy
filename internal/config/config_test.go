package config

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, text, env string) *Config {
	t.Helper()
	cfg, err := Parse(text, env)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg
}

func TestTemplateMatchesDefaults(t *testing.T) {
	if a, b := mustParse(t, Template, "25570"), mustParse(t, "", "25570"); !reflect.DeepEqual(a, b) {
		t.Fatalf("template %+v\ndefaults %+v", a, b)
	}
}

func TestPublicAddress(t *testing.T) {
	if c := mustParse(t, "", "25570"); c.ListenHost != "0.0.0.0" || c.ListenPort != 25570 {
		t.Fatalf("SERVER_PORT not used: %s:%d", c.ListenHost, c.ListenPort)
	}
	if c := mustParse(t, "", ""); c.ListenPort != 25565 {
		t.Fatalf("default port %d", c.ListenPort)
	}
	if c := mustParse(t, `public_address = "127.0.0.1:30000"`, "25570"); c.ListenHost != "127.0.0.1" || c.ListenPort != 30000 {
		t.Fatalf("explicit address: %s:%d", c.ListenHost, c.ListenPort)
	}
}

func TestSecondsAcceptIntegersAndFloats(t *testing.T) {
	c := mustParse(t, "hold_timeout = 0.5\nsleep_after = 10", "")
	if c.HoldTimeout != 500*time.Millisecond || c.SleepAfter != 10*time.Second {
		t.Fatalf("got %v %v", c.HoldTimeout, c.SleepAfter)
	}
}

func TestRejectsInvalidConfig(t *testing.T) {
	cases := map[string]string{
		`hold_timeout = "25"`:                 "hold_timeout",
		`hold_timeout = -1`:                   "positive",
		"[backend]\nport = 70000":             "backend.port",
		`public_address = "nope"`:             "public_address",
		`holdtimeout = 3`:                     `unknown setting "holdtimeout"`,
		"[motd]\nfoo = 1":                     `unknown setting "motd.foo"`,
		`public_address = "0.0.0.0:25566"`:    "must differ",
		"[backend]\ncommand = \"  \"":         "must not be empty",
		"[backend]\nproxy_protocol = \"yes\"": "proxy_protocol",
		"[motd]\nsource = \"paper\"":          "motd.source",
		`this is not toml`:                    "",
	}
	for text, want := range cases {
		_, err := Parse(text, "")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got error %v, want it to contain %q", text, err, want)
		}
	}
}
