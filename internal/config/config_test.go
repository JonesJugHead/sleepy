package config

import (
	"os"
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
	if a, b := mustParse(t, Template(TypeProcess), "25570"), mustParse(t, "", "25570"); !reflect.DeepEqual(a, b) {
		t.Fatalf("template %+v\ndefaults %+v", a, b)
	}
	// The docker template only differs by its type and address.
	docker := mustParse(t, Template(TypeDocker), "25570")
	want := mustParse(t, "[backend]\ntype = \"docker\"\naddress = \"mc:25566\"", "25570")
	if !reflect.DeepEqual(docker, want) {
		t.Fatalf("docker template %+v\nwant %+v", docker, want)
	}
}

func TestBackendTypes(t *testing.T) {
	c := mustParse(t, "", "")
	if c.Backend.Type != TypeProcess || c.Backend.Host != "127.0.0.1" || c.Backend.Port != 25566 {
		t.Fatalf("default backend %+v", c.Backend)
	}
	// docker: the server may use the same port as Sleepy, on another host.
	c = mustParse(t, "[backend]\ntype = \"docker\"\naddress = \"mc:25565\"\ncontainer = \" mc \"\ndocker_endpoint = \"tcp://proxy:2375\"", "")
	if c.Backend.Host != "mc" || c.Backend.Port != 25565 || c.Backend.Container != "mc" || c.Backend.DockerEndpoint != "tcp://proxy:2375" {
		t.Fatalf("docker backend %+v", c.Backend)
	}
	c = mustParse(t, "[backend]\naddress = \"[::1]:25570\"", "")
	if c.Backend.Host != "::1" || c.Backend.Port != 25570 {
		t.Fatalf("IPv6 address %+v", c.Backend)
	}
}

func TestPowerAndBusy(t *testing.T) {
	c := mustParse(t, "busy_command = \"test -f x\"\nbusy_retry = 60\n[power]\ndirectory = \"/var/lib/mc-power\"", "")
	if c.BusyCommand != "test -f x" || c.BusyRetry != time.Minute || c.BusyMaxDelay != 3*time.Hour || c.Power.Directory != "/var/lib/mc-power" {
		t.Fatalf("got %+v", c)
	}
	if c.HoldTimeout != 180*time.Second {
		t.Fatalf("hold_timeout default %v", c.HoldTimeout)
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
		`hold_timeout = "25"`:                            "hold_timeout",
		`hold_timeout = -1`:                              "positive",
		"[backend]\naddress = \"x:70000\"":               "backend.address has an invalid port",
		"[backend]\naddress = \"25566\"":                 "backend.address must look like",
		"[backend]\ntype = \"ptero\"":                    "backend.type",
		"[backend]\ntype = \"docker\"\ncontainer = \"\"": "backend.container",
		"busy_command = \"x\"\nbusy_retry = 0":           "busy_retry",
		"[backend]\nport = 25566":                        `unknown setting "backend.port"`,
		`public_address = "nope"`:                        "public_address",
		`holdtimeout = 3`:                                `unknown setting "holdtimeout"`,
		"[motd]\nfoo = 1":                                `unknown setting "motd.foo"`,
		`public_address = "0.0.0.0:25566"`:               "must differ",
		"[backend]\ncommand = \"  \"":                    "must not be empty",
		"[backend]\nproxy_protocol = \"yes\"":            "proxy_protocol",
		"[motd]\nsource = \"paper\"":                     "motd.source",
		`this is not toml`:                               "",
	}
	for text, want := range cases {
		_, err := Parse(text, "")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: got error %v, want it to contain %q", text, err, want)
		}
	}
}

func TestExampleConfig(t *testing.T) {
	text, err := os.ReadFile("../../examples/sleepy.docker.toml")
	if err != nil {
		t.Fatal(err)
	}
	c := mustParse(t, string(text), "")
	if c.Backend.Type != TypeDocker || c.Backend.Host != "mc" || c.Power.Directory != "/var/lib/mc-power" {
		t.Fatalf("example %+v", c)
	}
}
