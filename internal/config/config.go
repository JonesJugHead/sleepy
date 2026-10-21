// Package config loads and validates sleepy.toml.
package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type Backend struct {
	Command string
	// Port of the Minecraft server, always bound to 127.0.0.1.
	Port      int
	Directory string
	// ProxyProtocol is "auto" (Paper and forks), "on" or "off": whether the players' real IP is
	// forwarded to the server with the PROXY protocol.
	ProxyProtocol string
}

type Motd struct {
	Sleeping    string
	Starting    string
	VersionName string
	// Source is "sleepy" (the MOTDs above) or "server" (the real server's MOTD, remembered).
	Source string
}

type Config struct {
	ListenHost        string
	ListenPort        int
	Backend           Backend
	Motd              Motd
	KickMessage       string
	HoldTimeout       time.Duration
	SleepAfter        time.Duration
	MinimumOnlineTime time.Duration
	StopTimeout       time.Duration
	WhitelistOnly     bool
}

// seconds accepts both TOML integers and floats.
type seconds float64

func (s *seconds) UnmarshalTOML(v any) error {
	switch n := v.(type) {
	case int64:
		*s = seconds(n)
	case float64:
		*s = seconds(n)
	default:
		return errors.New("must be a number of seconds")
	}
	if *s < 0 || math.IsInf(float64(*s), 0) || math.IsNaN(float64(*s)) {
		return errors.New("must be a positive number of seconds")
	}
	return nil
}

func (s seconds) duration() time.Duration { return time.Duration(float64(s) * float64(time.Second)) }

type rawConfig struct {
	PublicAddress     string  `toml:"public_address"`
	KickMessage       string  `toml:"kick_message"`
	HoldTimeout       seconds `toml:"hold_timeout"`
	SleepAfter        seconds `toml:"sleep_after"`
	MinimumOnlineTime seconds `toml:"minimum_online_time"`
	StopTimeout       seconds `toml:"stop_timeout"`
	WhitelistOnly     bool    `toml:"whitelist_only"`
	Backend           struct {
		Command       string `toml:"command"`
		Port          int    `toml:"port"`
		Directory     string `toml:"directory"`
		ProxyProtocol string `toml:"proxy_protocol"`
	} `toml:"backend"`
	Motd struct {
		Sleeping    string `toml:"sleeping"`
		Starting    string `toml:"starting"`
		VersionName string `toml:"version_name"`
		Source      string `toml:"source"`
	} `toml:"motd"`
}

func defaults() rawConfig {
	var r rawConfig
	r.KickMessage = "The server is starting, reconnect in a few seconds"
	r.HoldTimeout, r.SleepAfter, r.MinimumOnlineTime, r.StopTimeout = 25, 300, 60, 60
	r.Backend.Command = "java -Xms128M -XX:MaxRAMPercentage=85.0 -jar server.jar nogui"
	r.Backend.Port = 25566
	r.Backend.Directory = "."
	r.Backend.ProxyProtocol = "auto"
	r.Motd.Sleeping = "§7Server is asleep §8- §ajoin to wake it up"
	r.Motd.Starting = "§eServer is starting..."
	r.Motd.VersionName = "Sleepy"
	r.Motd.Source = "sleepy"
	return r
}

// Load reads the config file. If it does not exist and create is set, it writes the commented
// template and returns generated = true.
func Load(path string, create bool) (cfg *Config, generated bool, err error) {
	text, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && create {
		return nil, true, os.WriteFile(path, []byte(Template), 0o644)
	}
	if err != nil {
		return nil, false, err
	}
	cfg, err = Parse(string(text), os.Getenv("SERVER_PORT"))
	return cfg, false, err
}

// Parse validates the TOML text and applies defaults. envServerPort is Pterodactyl's SERVER_PORT.
func Parse(text, envServerPort string) (*Config, error) {
	raw := defaults()
	md, err := toml.Decode(text, &raw)
	if err != nil {
		return nil, err
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		return nil, fmt.Errorf("unknown setting %q", undecoded[0].String())
	}

	host, port, err := parseAddress(raw.PublicAddress, envServerPort)
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		ListenHost: host,
		ListenPort: port,
		Backend: Backend{
			Command:       raw.Backend.Command,
			Port:          raw.Backend.Port,
			Directory:     raw.Backend.Directory,
			ProxyProtocol: raw.Backend.ProxyProtocol,
		},
		Motd:              Motd{Sleeping: raw.Motd.Sleeping, Starting: raw.Motd.Starting, VersionName: raw.Motd.VersionName, Source: raw.Motd.Source},
		KickMessage:       raw.KickMessage,
		HoldTimeout:       raw.HoldTimeout.duration(),
		SleepAfter:        raw.SleepAfter.duration(),
		MinimumOnlineTime: raw.MinimumOnlineTime.duration(),
		StopTimeout:       raw.StopTimeout.duration(),
		WhitelistOnly:     raw.WhitelistOnly,
	}
	if strings.TrimSpace(cfg.Backend.Command) == "" {
		return nil, errors.New("backend.command must not be empty")
	}
	if !validPort(cfg.Backend.Port) {
		return nil, errors.New("backend.port must be a port between 1 and 65535")
	}
	if cfg.Motd.Source != "sleepy" && cfg.Motd.Source != "server" {
		return nil, fmt.Errorf("motd.source must be \"sleepy\" or \"server\", got %q", cfg.Motd.Source)
	}
	switch cfg.Backend.ProxyProtocol {
	case "auto", "on", "off":
	default:
		return nil, fmt.Errorf("backend.proxy_protocol must be \"auto\", \"on\" or \"off\", got %q", cfg.Backend.ProxyProtocol)
	}
	if cfg.Backend.Port == cfg.ListenPort {
		return nil, fmt.Errorf("backend.port (%d) must differ from the public port", cfg.Backend.Port)
	}
	return cfg, nil
}

var addressPattern = regexp.MustCompile(`^(.+):(\d+)$`)

func parseAddress(value, envServerPort string) (string, int, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if envServerPort == "" {
			return "0.0.0.0", 25565, nil
		}
		port, err := strconv.Atoi(envServerPort)
		if err != nil || !validPort(port) {
			return "", 0, fmt.Errorf("SERVER_PORT environment variable is not a valid port: %q", envServerPort)
		}
		return "0.0.0.0", port, nil
	}
	m := addressPattern.FindStringSubmatch(value)
	if m == nil {
		return "", 0, fmt.Errorf("public_address must look like \"0.0.0.0:25565\", got %q", value)
	}
	port, err := strconv.Atoi(m[2])
	if err != nil || !validPort(port) {
		return "", 0, fmt.Errorf("public_address has an invalid port: %q", value)
	}
	return strings.TrimSuffix(strings.TrimPrefix(m[1], "["), "]"), port, nil
}

func validPort(p int) bool { return p >= 1 && p <= 65535 }
