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

// Backend types: how Sleepy starts and stops the Minecraft server.
const (
	// TypeProcess: Sleepy runs the server command itself (Pterodactyl, systemd, screen...).
	TypeProcess = "process"
	// TypeDocker: the server is an existing Docker container that Sleepy starts and stops.
	TypeDocker = "docker"
)

type Backend struct {
	Type string
	// Host and Port: where Sleepy reaches the Minecraft server.
	Host      string
	Port      int
	Directory string
	// ProxyProtocol is "auto" (Paper and forks), "on" or "off": whether the players' real IP is
	// forwarded to the server with the PROXY protocol.
	ProxyProtocol string
	// Command is the server command (process type).
	Command string
	// Container and DockerEndpoint (docker type): the container name or ID, and the Docker API
	// address ("" = $DOCKER_HOST, else the local socket).
	Container      string
	DockerEndpoint string
}

type Motd struct {
	Sleeping    string
	Starting    string
	Stopped     string
	VersionName string
	// Source is "sleepy" (the MOTDs above) or "server" (the real server's MOTD, remembered).
	Source string
}

// Power is the optional state directory shared with a panel and backup scripts.
type Power struct {
	// Directory is "" when the feature is off.
	Directory      string
	StoppedMessage string
	BackupMessage  string
	RestoreMessage string
}

type Config struct {
	ListenHost        string
	ListenPort        int
	Backend           Backend
	Motd              Motd
	Power             Power
	KickMessage       string
	HoldTimeout       time.Duration
	SleepAfter        time.Duration
	MinimumOnlineTime time.Duration
	StopTimeout       time.Duration
	WhitelistOnly     bool
	// BusyCommand, if set, is run before an idle stop: exit code 0 means the server is busy and
	// the stop is retried after BusyRetry, for at most BusyMaxDelay in total.
	BusyCommand  string
	BusyRetry    time.Duration
	BusyMaxDelay time.Duration
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
	BusyCommand       string  `toml:"busy_command"`
	BusyRetry         seconds `toml:"busy_retry"`
	BusyMaxDelay      seconds `toml:"busy_max_delay"`
	Backend           struct {
		Type           string `toml:"type"`
		Address        string `toml:"address"`
		Directory      string `toml:"directory"`
		ProxyProtocol  string `toml:"proxy_protocol"`
		Command        string `toml:"command"`
		Container      string `toml:"container"`
		DockerEndpoint string `toml:"docker_endpoint"`
	} `toml:"backend"`
	Motd struct {
		Sleeping    string `toml:"sleeping"`
		Starting    string `toml:"starting"`
		Stopped     string `toml:"stopped"`
		VersionName string `toml:"version_name"`
		Source      string `toml:"source"`
	} `toml:"motd"`
	Power struct {
		Directory      string `toml:"directory"`
		StoppedMessage string `toml:"stopped_message"`
		BackupMessage  string `toml:"backup_message"`
		RestoreMessage string `toml:"restore_message"`
	} `toml:"power"`
}

func defaults() rawConfig {
	var r rawConfig
	r.KickMessage = "The server is starting, reconnect in a few seconds"
	r.HoldTimeout, r.SleepAfter, r.MinimumOnlineTime, r.StopTimeout = 180, 300, 60, 60
	r.BusyRetry, r.BusyMaxDelay = 900, 10800
	r.Backend.Type = TypeProcess
	r.Backend.Address = "127.0.0.1:25566"
	r.Backend.Directory = "."
	r.Backend.ProxyProtocol = "auto"
	r.Backend.Command = "java -Xms128M -XX:MaxRAMPercentage=85.0 -jar server.jar nogui"
	r.Backend.Container = "mc"
	r.Motd.Sleeping = "§7Server is asleep §8- §ajoin to wake it up"
	r.Motd.Starting = "§eServer is starting..."
	r.Motd.Stopped = "§cServer stopped by an administrator"
	r.Motd.VersionName = "Sleepy"
	r.Motd.Source = "sleepy"
	r.Power.StoppedMessage = "The server was stopped by an administrator"
	r.Power.BackupMessage = "Backup in progress, try again in a minute"
	r.Power.RestoreMessage = "Restore in progress, try again in a few minutes"
	return r
}

// Load reads the config file. If it does not exist and create is set, it writes the commented
// template for the detected environment and returns generated = true.
func Load(path string, create bool) (cfg *Config, generated bool, err error) {
	text, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && create {
		return nil, true, os.WriteFile(path, []byte(Template(DetectType())), 0o644)
	}
	if err != nil {
		return nil, false, err
	}
	cfg, err = Parse(string(text), os.Getenv("SERVER_PORT"))
	return cfg, false, err
}

// DockerSocket is where the Docker API is looked for when DOCKER_HOST is not set.
const DockerSocket = "/var/run/docker.sock"

// DetectType guesses the backend type for a new sleepy.toml: Pterodactyl runs the server inside
// Sleepy's own container; elsewhere, access to a Docker API means the server is a container.
func DetectType() string {
	if os.Getenv("P_SERVER_UUID") != "" {
		return TypeProcess
	}
	if os.Getenv("DOCKER_HOST") != "" {
		return TypeDocker
	}
	if _, err := os.Stat(DockerSocket); err == nil {
		return TypeDocker
	}
	return TypeProcess
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
	backendHost, backendPort, err := parseBackendAddress(raw.Backend.Address)
	if err != nil {
		return nil, err
	}
	cfg := &Config{
		ListenHost: host,
		ListenPort: port,
		Backend: Backend{
			Type:           raw.Backend.Type,
			Host:           backendHost,
			Port:           backendPort,
			Directory:      raw.Backend.Directory,
			ProxyProtocol:  raw.Backend.ProxyProtocol,
			Command:        raw.Backend.Command,
			Container:      strings.TrimSpace(raw.Backend.Container),
			DockerEndpoint: strings.TrimSpace(raw.Backend.DockerEndpoint),
		},
		Motd: Motd{Sleeping: raw.Motd.Sleeping, Starting: raw.Motd.Starting, Stopped: raw.Motd.Stopped,
			VersionName: raw.Motd.VersionName, Source: raw.Motd.Source},
		Power: Power{Directory: strings.TrimSpace(raw.Power.Directory), StoppedMessage: raw.Power.StoppedMessage,
			BackupMessage: raw.Power.BackupMessage, RestoreMessage: raw.Power.RestoreMessage},
		KickMessage:       raw.KickMessage,
		HoldTimeout:       raw.HoldTimeout.duration(),
		SleepAfter:        raw.SleepAfter.duration(),
		MinimumOnlineTime: raw.MinimumOnlineTime.duration(),
		StopTimeout:       raw.StopTimeout.duration(),
		WhitelistOnly:     raw.WhitelistOnly,
		BusyCommand:       strings.TrimSpace(raw.BusyCommand),
		BusyRetry:         raw.BusyRetry.duration(),
		BusyMaxDelay:      raw.BusyMaxDelay.duration(),
	}
	switch cfg.Backend.Type {
	case TypeProcess:
		if strings.TrimSpace(cfg.Backend.Command) == "" {
			return nil, errors.New("backend.command must not be empty")
		}
		if cfg.Backend.Port == cfg.ListenPort {
			return nil, fmt.Errorf("the port of backend.address (%d) must differ from the public port", cfg.Backend.Port)
		}
	case TypeDocker:
		if cfg.Backend.Container == "" {
			return nil, errors.New("backend.container must not be empty")
		}
	default:
		return nil, fmt.Errorf("backend.type must be %q or %q, got %q", TypeProcess, TypeDocker, cfg.Backend.Type)
	}
	if cfg.Motd.Source != "sleepy" && cfg.Motd.Source != "server" {
		return nil, fmt.Errorf("motd.source must be \"sleepy\" or \"server\", got %q", cfg.Motd.Source)
	}
	switch cfg.Backend.ProxyProtocol {
	case "auto", "on", "off":
	default:
		return nil, fmt.Errorf("backend.proxy_protocol must be \"auto\", \"on\" or \"off\", got %q", cfg.Backend.ProxyProtocol)
	}
	if cfg.BusyCommand != "" && cfg.BusyRetry <= 0 {
		return nil, errors.New("busy_retry must be more than 0")
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
	return splitAddress("public_address", value)
}

func parseBackendAddress(value string) (string, int, error) {
	return splitAddress("backend.address", strings.TrimSpace(value))
}

func splitAddress(key, value string) (string, int, error) {
	m := addressPattern.FindStringSubmatch(value)
	if m == nil {
		return "", 0, fmt.Errorf("%s must look like \"host:port\", got %q", key, value)
	}
	port, err := strconv.Atoi(m[2])
	if err != nil || !validPort(port) {
		return "", 0, fmt.Errorf("%s has an invalid port: %q", key, value)
	}
	return strings.TrimSuffix(strings.TrimPrefix(m[1], "["), "]"), port, nil
}

func validPort(p int) bool { return p >= 1 && p <= 65535 }
