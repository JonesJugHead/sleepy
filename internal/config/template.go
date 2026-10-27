package config

import "strings"

// Template returns the sleepy.toml written on first launch for a backend type. Keep in sync with
// defaults(): only backend.type and backend.address differ between the two types.
func Template(backendType string) string {
	address := "127.0.0.1:25566"
	if backendType == TypeDocker {
		address = "mc:25566"
	}
	return strings.NewReplacer("{{type}}", backendType, "{{address}}", address).Replace(template)
}

const template = `# Sleepy configuration.
# All paths are relative to the current directory (the Pterodactyl server root, or /data in the
# Docker image).

# Public address Sleepy listens on (the port allocated by Pterodactyl).
# Leave empty to use 0.0.0.0 and the SERVER_PORT variable set by Pterodactyl (25565 if unset).
public_address = ""

# Sent to a waiting player when the server did not become ready within hold_timeout,
# and to players who are not allowed to wake the server (whitelist_only).
kick_message = "The server is starting, reconnect in a few seconds"

# Seconds a connecting player is held while the server starts. Sleepy keeps 1.13+ clients from
# timing out while they wait, so this can cover a slow modded start; older clients give up after 30.
hold_timeout = 180

# Seconds with no players before the server is stopped.
sleep_after = 300

# Never stop a server that has been running for less than this many seconds.
minimum_online_time = 60

# Seconds the server gets to stop cleanly. process: after "stop" on the console, SIGTERM, then
# SIGKILL 10 s later. docker: passed to the Docker stop (SIGTERM, then SIGKILL by Docker).
stop_timeout = 60

# Only players listed in whitelist.json can wake the server up.
whitelist_only = false

# Optional command run (through sh, in the current directory) when the server is about to sleep:
# exit code 0 means "busy" (a map render, a backup...) and the stop is retried after busy_retry
# seconds, for at most busy_max_delay seconds in total. Empty: no check.
busy_command = ""
busy_retry = 900
busy_max_delay = 10800

[backend]
# How Sleepy runs the Minecraft server:
#   "process": Sleepy runs backend.command itself (Pterodactyl, systemd, screen...).
#   "docker":  the server is an existing container (backend.container) that Sleepy starts and
#              stops through the Docker API.
type = "{{type}}"
# Where Sleepy reaches the Minecraft server, as host:port.
#   process: written to server.properties (server-ip, server-port) before each start.
#   docker:  the container on a shared Docker network, e.g. "mc:25566". Never written anywhere.
address = "{{address}}"
# Server directory: server.properties, whitelist.json, server-icon.png. Required for process.
# docker: optional, read only (mount the server data read-only to use it).
directory = "."
# Forward the players' real IP to the server with the PROXY protocol, so that logs, /ban-ip and
# IP-based plugins see it instead of Sleepy's address.
#   "auto": for Paper and its forks (config/paper-global.yml present). process: Sleepy enables
#           proxies.proxy-protocol there before each start. docker: follows what the file says.
#   "on":   always forward (the server must expect it, e.g. through a mod).
#   "off":  never forward (process: also disables it in paper-global.yml if present).
proxy_protocol = "auto"
# process: command that starts the Minecraft server, run through sh in backend.directory.
# MaxRAMPercentage sizes the heap from the Pterodactyl memory limit, leaving room for the JVM itself.
command = "java -Xms128M -XX:MaxRAMPercentage=85.0 -jar server.jar nogui"
# docker: name or ID of the server container.
container = "mc"
# docker: Docker API address. Empty: $DOCKER_HOST, else unix:///var/run/docker.sock.
# Examples: "unix:///var/run/docker.sock", "tcp://docker-socket-proxy:2375".
docker_endpoint = ""

[motd]
# Which MOTD the server list shows while the server is asleep or starting:
#   "sleepy": the messages below.
#   "server": the real server's MOTD. Sleepy remembers it each time the server runs (plugins and hex
#             colors included) and falls back to motd= in server.properties until then.
source = "sleepy"
# Shown in the server list while the server is asleep / starting. Supports § color codes and \n.
sleeping = "§7Server is asleep §8- §ajoin to wake it up"
starting = "§eServer is starting..."
# Shown while an administrator keeps the server stopped (see [power]), whatever the source.
stopped = "§cServer stopped by an administrator"
# Version name shown in the server list while Sleepy answers on behalf of the server.
version_name = "Sleepy"

[power]
# Optional directory shared with a panel and backup scripts. Empty: off. When set, Sleepy:
#   - never wakes the server while admin.json says {"mode": "stopped"};
#   - never wakes it while backup.lock or restore.lock exists (ignored after 6 hours);
#   - stays awake while keep-awake says {"until": "<ISO 8601 date>"} is in the future;
#   - writes sleepy.json: {"state": "awake|sleeping|waking", "since", "heartbeat", "version"},
#     with a heartbeat at least every 30 seconds.
directory = ""
# Sent to players who try to join in those cases.
stopped_message = "The server was stopped by an administrator"
backup_message = "Backup in progress, try again in a minute"
restore_message = "Restore in progress, try again in a few minutes"
`
