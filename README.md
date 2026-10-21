# Sleepy

A tiny sleep/wake wrapper for Minecraft Java servers running on Pterodactyl.

Sleepy listens on the public port and keeps Java turned off while nobody is playing. It starts the
server when a player connects and stops it again after a period of inactivity. While the server
sleeps, only Sleepy runs: about 4 MB of RAM, for a ~3.4 MB static binary.

It works with every Java client: a server-list ping never wakes the server up, and the server never
shows up as incompatible while it sleeps.

## Why

A small server for a group of friends is empty most of the day, yet a Java server idles at several
gigabytes of RAM. On a VPS or a Pterodactyl panel, that memory is paid for 24/7. Sleepy gives it
back whenever nobody is online, without players having to do anything special: they just connect,
wait a few seconds, and play.

## Screenshot / GIF

<!-- TODO: add a GIF showing the server list while asleep, a player joining, and the server waking up. -->
_Coming soon._

## Build

Written in Go. If Go is not installed, the scripts fall back to the `golang` Docker image.

```sh
./test.sh            # go vet + tests (Linux)
./build.sh           # -> dist/sleepy-v<VERSION>: static Linux x64 binary, runs on any x86-64 CPU
```

## Installation on Pterodactyl

Designed for the standard Minecraft Java egg (image `ghcr.io/pterodactyl/yolks:java_25`).

1. **Upload the binary** `dist/sleepy-v0.0.x` to the server root, **rename it to `sleepy`**, then
   make it executable: in the file manager, open the file's `⋯` menu → **Permissions** → `755`.
   Uploading clears the executable bit, so repeat this step after every new upload.
2. **Egg → Configuration Files**: replace the content with `{}`. Otherwise Pterodactyl rewrites
   `server-port` in `server.properties` on every start, while Sleepy sets it to the local port.
3. **Egg → Start Configuration**: `{"done": "[sleepy] Listening on "}`
4. **Egg → Stop Command**: `^C`
5. Server **startup command**:

   ```
   ./sleepy
   ```

   Do not chain other commands (for example `chmod +x sleepy && ./sleepy`): the yolks entrypoint
   does not run the command through a shell, and `&&` breaks the console and the "done" detection.
6. **First start**: Sleepy writes `sleepy.toml` with commented defaults, then exits.
   Check `backend.command` (jar name, memory), then start again.

When `public_address` is empty, the port allocated by Pterodactyl is read from the `SERVER_PORT`
variable. The Minecraft server itself listens on `127.0.0.1:<backend.port>` and is not reachable
from outside.

## Configuration

`sleepy.toml` in the current directory. All paths are relative to that directory.

To write it without mistakes, open `tools/config-generator.html` in a browser. This offline page
offers a form, an in-game preview of the MOTD, validation, import of an existing file, and a
download of the resulting `sleepy.toml`.

| Key | Default | Purpose |
| --- | --- | --- |
| `public_address` | `""` | Listen address `ip:port`. Empty = `0.0.0.0:$SERVER_PORT` (25565 if unset). |
| `kick_message` | "The server is starting, reconnect in a few seconds" | Sent if the server is not ready in time, or to a non-whitelisted player with `whitelist_only`. |
| `hold_timeout` | `25` | Seconds a connecting player is held while the server starts (the client gives up around 30). |
| `sleep_after` | `300` | Seconds without players before the server is stopped. |
| `minimum_online_time` | `60` | Minimum running time before an idle stop. |
| `stop_timeout` | `60` | Delay after `stop` before SIGTERM (then SIGKILL 10 s later). |
| `whitelist_only` | `false` | Only players in `whitelist.json` can wake the server. |
| `backend.command` | `java -Xms128M -XX:MaxRAMPercentage=85.0 -jar server.jar nogui` | Start command, run through `sh` (variables such as `${SERVER_MEMORY}` are expanded). |
| `backend.port` | `25566` | Local port of the Minecraft server. |
| `backend.directory` | `"."` | Server directory (`server.properties`, `whitelist.json`, `server-icon.png`). |
| `backend.proxy_protocol` | `"auto"` | Forward the players' real IP (PROXY protocol): `auto`, `on` or `off`. See below. |
| `motd.source` | `"sleepy"` | MOTD shown while asleep and starting: `"sleepy"` (the two messages below) or `"server"` (the real server MOTD, see below). |
| `motd.sleeping` | "Server is asleep - join to wake it up" | MOTD shown while the server sleeps. Supports `§` codes and `\n`. |
| `motd.starting` | "Server is starting..." | MOTD shown while the server starts. |
| `motd.version_name` | `"Sleepy"` | Version name shown in the server list. |

Before each start, Sleepy writes `server-port = backend.port` and `server-ip = 127.0.0.1` to
`server.properties`, leaving every other line untouched. `max-players` is read from it for the
server list.

### Showing the real server MOTD

With `motd.source = "server"`, the server list shows the Minecraft server's own MOTD, even while it
sleeps. Sleepy reads it from the server while it runs (as soon as it is ready, then every minute),
with its colors (hex included) and any changes made by plugins, and stores it in
`sleepy-state.json`. Before the very first start, it uses `motd=` from `server.properties`.

### Real player IP (PROXY protocol)

Without it, the server sees every player coming from `127.0.0.1`, since they go through Sleepy.
Logs, `/ban-ip` and IP-based plugins then stop working.

- `auto` (default): if `config/paper-global.yml` exists (Paper, Purpur, Folia...), Sleepy sets
  `proxies.proxy-protocol` to `true` there before each start, then forwards the IP. The file only
  exists after Paper's first start, so the IP is forwarded from the following start onwards.
  On other servers (vanilla, Spigot, Fabric...), nothing changes.
- `on`: always forward the IP. The server must expect it (for example through a Fabric mod),
  otherwise every connection fails.
- `off`: never forward the IP, and set `proxy-protocol` back to `false` in `paper-global.yml` if present.

Paper only listens on `127.0.0.1`: nobody but Sleepy can send it a spoofed IP.

## Console

`sleepy ...` commands are handled by Sleepy and **never forwarded to the server**, whether it is
running or not:

| Command | Effect |
| --- | --- |
| `sleepy status` | State, connected players, auto-sleep, IP forwarding. |
| `sleepy off` | Disables auto-sleep: the server stays on. Survives restarts (`sleepy-state.json`). |
| `sleepy on` | Re-enables auto-sleep. |
| `sleepy sleep` | Stops the server now. |
| `sleepy wake` | Starts the server now. |
| `sleepy reload` | Re-reads `sleepy.toml`. The command, port and directory apply on the next Java start; `public_address` requires restarting Sleepy. |
| `sleepy help` | Lists the commands. |

Otherwise:
- **Java running**: the console behaves exactly as without Sleepy. Everything goes to Java;
  `stop` goes through Sleepy to keep the SIGTERM/SIGKILL fallback.
- **Java stopped**: `start` starts the server, `stop` exits Sleepy, `status` shows the state,
  `help` lists the commands.

Sleepy's own lines are prefixed with `[sleepy]`.

## Development

```sh
./test.sh -race      # unit tests (protocol, config) and integration tests (fake server)
./test.sh -run Proxy # a subset
```

Integration tests re-run the test binary itself as a fake Minecraft server (`internal/testutil`).

Layout: `internal/config` (TOML), `internal/protocol` (VarInt, packets, legacy ping, PROXY v2
header), `internal/proxy` (listener, status, relay), `internal/backend` (Java process, states,
`server.properties`, `paper-global.yml`, whitelist), `internal/settings` (reloadable settings,
persistent state), `internal/console`, `internal/idle`, `main.go`.
The version is defined only in the `VERSION` file.

## License

[MIT](LICENSE) © JonesJugHead
