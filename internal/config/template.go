package config

// Template is written to sleepy.toml on first launch. Keep in sync with defaults().
const Template = `# Sleepy configuration.
# All paths are relative to the current directory (the Pterodactyl server root).

# Public address Sleepy listens on (the port allocated by Pterodactyl).
# Leave empty to use 0.0.0.0 and the SERVER_PORT variable set by Pterodactyl (25565 if unset).
public_address = ""

# Sent to a waiting player when the server did not become ready within hold_timeout,
# and to players who are not allowed to wake the server (whitelist_only).
kick_message = "The server is starting, reconnect in a few seconds"

# Seconds a connecting player is held while the server starts (the client gives up around 30).
hold_timeout = 25

# Seconds with no players before the server is stopped.
sleep_after = 300

# Never stop a server that has been running for less than this many seconds.
minimum_online_time = 60

# Seconds to wait after sending "stop" before sending SIGTERM (then SIGKILL as a last resort).
stop_timeout = 60

# Only players listed in whitelist.json can wake the server up.
whitelist_only = false

[backend]
# Command that starts the Minecraft server, run through sh in the backend directory.
# MaxRAMPercentage sizes the heap from the Pterodactyl memory limit, leaving room for the JVM itself.
command = "java -Xms128M -XX:MaxRAMPercentage=85.0 -jar server.jar nogui"
# Local port the Minecraft server listens on (written to server.properties, bound to 127.0.0.1).
port = 25566
# Directory containing server.properties, server.jar, whitelist.json and server-icon.png.
directory = "."
# Forward the players' real IP to the server with the PROXY protocol, so that logs, /ban-ip and
# IP-based plugins see it instead of 127.0.0.1.
#   "auto": for Paper and its forks (config/paper-global.yml present), Sleepy enables
#           proxies.proxy-protocol there before each start and forwards the IP.
#   "on":   always forward (the server must expect it, e.g. a Fabric mod).
#   "off":  never forward (and disable it in paper-global.yml if present).
proxy_protocol = "auto"

[motd]
# Which MOTD the server list shows while the server is asleep or starting:
#   "sleepy": the two messages below.
#   "server": the real server's MOTD. Sleepy remembers it each time the server runs (plugins and hex
#             colors included) and falls back to motd= in server.properties until then.
source = "sleepy"
# Shown in the server list while the server is asleep / starting. Supports § color codes and \n.
sleeping = "§7Server is asleep §8- §ajoin to wake it up"
starting = "§eServer is starting..."
# Version name shown in the server list while Sleepy answers on behalf of the server.
version_name = "Sleepy"
`
