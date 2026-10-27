# Sleepy

A tiny sleep/wake wrapper for Minecraft Java servers, on Pterodactyl or in Docker.

Sleepy listens on the public port and keeps the server turned off while nobody is playing. It starts
the server when a player connects and stops it again after a period of inactivity. While the server
sleeps, only Sleepy runs: about 4 MB of RAM (6 MB measured in Docker), for a ~4 MB static binary.

It works with every Java client: a server-list ping never wakes the server up, and the server never
shows up as incompatible while it sleeps. Modded servers (NeoForge, Forge, Fabric, Arclight...) are
supported: players are kept waiting through a start of several minutes.

## Why

A small server for a group of friends is empty most of the day, yet a Java server idles at several
gigabytes of RAM. On a VPS or a Pterodactyl panel, that memory is paid for 24/7. Sleepy gives it
back whenever nobody is online, without players having to do anything special: they just connect,
wait, and play.

## Screenshot / GIF

<!-- TODO: add a GIF showing the server list while asleep, a player joining, and the server waking up. -->
_Coming soon._

## Two ways to run it

`backend.type` in `sleepy.toml` says how Sleepy runs the Minecraft server:

| Type | Use it when | Sleepy... |
| --- | --- | --- |
| `process` | Pterodactyl, systemd, screen, any machine where Sleepy can run Java itself | runs `backend.command`, writes `server-port`/`server-ip` in `server.properties`, stops with `stop` then SIGTERM |
| `docker` | the server already runs in its own container (itzg/minecraft-server...) | starts and stops that container through the Docker API, never touches its files |

On first launch Sleepy writes a commented `sleepy.toml` and exits. The type is detected then:
`process` on Pterodactyl, `docker` when a Docker API is reachable (`DOCKER_HOST` or
`/var/run/docker.sock`), `process` otherwise. Check the file, then start Sleepy again.

## Build

Written in Go. If Go is not installed, the scripts fall back to the `golang` Docker image.

```sh
./test.sh            # go vet + tests (Linux)
./build.sh           # -> dist/sleepy-v<VERSION>: static Linux x64 binary, runs on any x86-64 CPU
docker build -t sleepy .   # the Docker image
```

## Installation on Pterodactyl (`process`)

Designed for the standard Minecraft Java egg (image `ghcr.io/pterodactyl/yolks:java_25`).

1. **Upload the binary** `dist/sleepy-v0.x.y` to the server root, **rename it to `sleepy`**, then
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
variable. The Minecraft server itself listens on `backend.address` (`127.0.0.1:25566`) and is not
reachable from outside.

## Installation with Docker (`docker`)

Sleepy runs in its own container, next to the server container, on a shared Docker network.
[`examples/docker-compose.yml`](examples/docker-compose.yml) and
[`examples/sleepy.docker.toml`](examples/sleepy.docker.toml) are a complete setup.

1. **Server container**: remove its `ports:` block (Sleepy publishes 25565 now), put it on a
   network Sleepy shares (`web` in the example), and give it `stdin_open: true` so console commands
   reach it. Its `server-port` stays whatever it is (`25566` in the example): Sleepy targets
   `backend.address = "mc:25566"`. Avoid `restart: always`, which would restart a container Sleepy
   put to sleep; `unless-stopped` is fine.
2. **Docker API access**: the example gives Sleepy a
   [docker-socket-proxy](https://github.com/Tecnativa/docker-socket-proxy) allowing only inspect,
   start and stop (plus attach for console commands), so Sleepy can run as an unprivileged user
   (`user: "1000:1000"`). Sleepy can also use the socket directly
   (`/var/run/docker.sock:/var/run/docker.sock` and `group_add` with the socket's group).
3. **Configuration**: `/opt/sleepy` (mounted as `/data`, owned by the container user) holds
   `sleepy.toml` and `sleepy-state.json`. Start once to get the template, or copy the example.
4. `docker compose up -d`, then `docker logs -f sleepy`.

How it behaves:

- A player connects → `POST /containers/mc/start`. Ready as soon as the server answers a status
  ping on `backend.address`.
- Idle → `POST /containers/mc/stop?t=<stop_timeout>`, like `docker stop --timeout`: the container
  gets its stop signal (itzg: a clean `stop` with `STOP_DURATION`), then SIGKILL from Docker only
  after `stop_timeout`. Sleepy never kills the server itself.
- The container started by someone else (a panel, `docker start mc`, Sleepy restarted while it
  runs) is taken over within 5 seconds: it is shown as awake and put to sleep like any other.
- Stopped by someone else: Sleepy notices within 2 seconds and goes back to sleeping.
- Sleepy itself stopping or restarting leaves the container as it is.
- The server's files are never written. `backend.directory` is optional and read-only: without the
  server files, the icon and max players shown while asleep come from the status Sleepy remembers.

The console: `docker attach sleepy` (detach with Ctrl+P Ctrl+Q). Lines that are not `sleepy ...`
commands go to the server's stdin while it runs.

## Configuration

`sleepy.toml` in the current directory. All paths are relative to that directory.

To write it without mistakes, open `tools/config-generator.html` in a browser. This offline page
offers a form, an in-game preview of the MOTD, validation, import of an existing file, and a
download of the resulting `sleepy.toml`.

| Key | Default | Purpose |
| --- | --- | --- |
| `public_address` | `""` | Listen address `ip:port`. Empty = `0.0.0.0:$SERVER_PORT` (25565 if unset). |
| `kick_message` | "The server is starting, reconnect in a few seconds" | Sent if the server is not ready in time, or to a non-whitelisted player with `whitelist_only`. |
| `hold_timeout` | `180` | Seconds a connecting player is held while the server starts. See [Long starts](#long-starts-modded-servers). |
| `sleep_after` | `300` | Seconds without players before the server is stopped. |
| `minimum_online_time` | `60` | Minimum running time before an idle stop. |
| `stop_timeout` | `60` | Seconds the server gets to stop. `process`: after `stop`, then SIGTERM, then SIGKILL 10 s later. `docker`: the Docker stop timeout. |
| `whitelist_only` | `false` | Only players in `whitelist.json` can wake the server. |
| `busy_command` | `""` | Run before an idle stop; exit code 0 = busy, the stop is postponed. See below. |
| `busy_retry` | `900` | Seconds between two checks while `busy_command` says busy. |
| `busy_max_delay` | `10800` | The stop happens anyway after being postponed this long. |
| `backend.type` | `"process"` | `"process"` or `"docker"`, see above. |
| `backend.address` | `"127.0.0.1:25566"` | Where the server is reached. `process`: written to `server.properties`. `docker`: e.g. `"mc:25566"`. |
| `backend.directory` | `"."` | Server directory (`server.properties`, `whitelist.json`, `server-icon.png`). Read only with `docker`. |
| `backend.proxy_protocol` | `"auto"` | Forward the players' real IP (PROXY protocol): `auto`, `on` or `off`. See below. |
| `backend.command` | `java -Xms128M -XX:MaxRAMPercentage=85.0 -jar server.jar nogui` | `process`: start command, run through `sh` (variables such as `${SERVER_MEMORY}` are expanded). |
| `backend.container` | `"mc"` | `docker`: name or ID of the server container. |
| `backend.docker_endpoint` | `""` | `docker`: `unix:///path` or `tcp://host:port`. Empty = `$DOCKER_HOST`, else `/var/run/docker.sock`. |
| `motd.source` | `"sleepy"` | MOTD shown while asleep and starting: `"sleepy"` (the messages below) or `"server"` (the real server MOTD, see below). |
| `motd.sleeping` | "Server is asleep - join to wake it up" | MOTD shown while the server sleeps. Supports `§` codes and `\n`. |
| `motd.starting` | "Server is starting..." | MOTD shown while the server starts. |
| `motd.stopped` | "Server stopped by an administrator" | MOTD shown while `[power]` keeps the server stopped. |
| `motd.version_name` | `"Sleepy"` | Version name shown in the server list. |
| `power.directory` | `""` | State directory shared with a panel and scripts. Empty = off. See below. |
| `power.stopped_message`, `power.backup_message`, `power.restore_message` | English messages | Sent to players who cannot wake the server for that reason. |

With `process`, before each start Sleepy writes `server-port` and `server-ip` from
`backend.address` to `server.properties`, leaving every other line untouched. `max-players` is
read from it for the server list.

### Long starts (modded servers)

A Minecraft client gives up on a connection that stays silent for 30 seconds, which is shorter than
the start of most modded servers. While a 1.13+ player waits, Sleepy sends it a login plugin request
every 10 seconds on a channel nobody knows (`sleepy:wait`); the client answers "not understood",
and its timeout starts over. Once the server is ready, Sleepy drops those answers and hands the
connection over as if nothing happened. The player sees "Logging in..." for the whole start.

Clients before 1.13 do not have this packet: they are held for at most 25 seconds, then asked to
reconnect.

### Showing the real server MOTD

With `motd.source = "server"`, the server list shows the Minecraft server's own MOTD, even while it
sleeps. Sleepy reads it from the server while it runs (as soon as it is ready, then every minute),
with its colors (hex included) and any changes made by plugins, and stores it in
`sleepy-state.json` together with the server icon and max players. Before the very first start, it
uses `motd=` from `server.properties`.

### Power directory (panel, backups)

`power.directory` is a directory Sleepy shares with a control panel and maintenance scripts. Files
written by others are read on every check; writes are expected to be atomic (write, then rename).

| File | Written by | Effect |
| --- | --- | --- |
| `admin.json` | panel, admin | `{"mode": "stopped"}`: an administrator stopped the server. Sleepy never wakes it, shows `motd.stopped` and sends `stopped_message`. `{"mode": "auto"}` or no file: normal. |
| `backup.lock` | backup script | While present, Sleepy does not start the server (`backup_message`). |
| `restore.lock` | restore job | Same, with `restore_message`. Locks older than 6 hours are ignored (crashed script). |
| `keep-awake` | panel, scripts | `{"until": "2026-10-28T04:00:00+01:00", "reason": "map render"}`: no idle stop before that date. |
| `sleepy.json` | Sleepy | `{"state": "awake" \| "sleeping" \| "waking", "since", "heartbeat", "version"}`, rewritten on every change and at least every 30 seconds. `waking` is a start triggered by a player or the console. |

The console commands `start` and `sleepy wake` respect the locks and `admin.json` too.

### Postponing sleep (`busy_command`)

When the server is about to sleep, Sleepy runs `busy_command` through `sh`. Exit code 0 means busy
(a map render, a long task): the stop is retried every `busy_retry` seconds, and happens anyway once
it has been postponed for `busy_max_delay`. The first line the command prints is logged as the
reason. A command running for more than a minute counts as "not busy".

### Real player IP (PROXY protocol)

Without it, the server sees every player coming from Sleepy's address (`127.0.0.1`, or the Sleepy
container's IP with `docker`). Logs, `/ban-ip` and IP-based plugins then stop working, and
Bukkit-based servers (Spigot, Arclight...) may throttle players joining close together: set
`connection-throttle: -1` in `bukkit.yml` there.

- `auto` (default): Paper and its forks (`config/paper-global.yml`). With `process`, Sleepy sets
  `proxies.proxy-protocol` to `true` there before each start, then forwards the IP; the file only
  exists after Paper's first start, so the IP is forwarded from the following start onwards. With
  `docker`, Sleepy forwards the IP if the file (in `backend.directory`) enables it. On other servers
  (vanilla, Spigot, Fabric, NeoForge...), nothing changes.
- `on`: always forward the IP. The server must expect it (for example through a mod),
  otherwise every connection fails.
- `off`: never forward the IP. With `process`, also sets `proxy-protocol` back to `false` in
  `paper-global.yml` if present.

Only expose the server to Sleepy (`127.0.0.1`, or a Docker network without published ports): a
server that accepts the PROXY header from anyone accepts spoofed IPs.

## Console

`sleepy ...` commands are handled by Sleepy and **never forwarded to the server**, whether it is
running or not:

| Command | Effect |
| --- | --- |
| `sleepy status` | State, connected players, auto-sleep, what blocks a wake-up, IP forwarding, backend. |
| `sleepy off` | Disables auto-sleep: the server stays on. Survives restarts (`sleepy-state.json`). |
| `sleepy on` | Re-enables auto-sleep. |
| `sleepy sleep` | Stops the server now. |
| `sleepy wake` | Starts the server now. |
| `sleepy reload` | Re-reads `sleepy.toml`. Backend settings apply on the next server start; `public_address` and `power.directory` require restarting Sleepy. |
| `sleepy help` | Lists the commands. |

Otherwise:
- **Server running**: the console behaves exactly as without Sleepy. Everything goes to the
  server; `stop` goes through Sleepy to keep its stop sequence.
- **Server stopped**: `start` starts the server, `stop` exits Sleepy, `status` shows the state,
  `help` lists the commands.

Sleepy's own lines are prefixed with `[sleepy]`.

## Development

```sh
./test.sh -race      # unit tests and integration tests (fake server, fake Docker API)
./test.sh -run Proxy # a subset
```

Integration tests re-run the test binary itself as a fake Minecraft server, and serve a fake Docker
Engine API whose container is that fake server (`internal/testutil`).

Layout: `internal/config` (TOML), `internal/protocol` (VarInt, packets, legacy ping, PROXY v2
header), `internal/proxy` (listener, status, hold, relay), `internal/backend` (states, process and
container instances, `server.properties`, `paper-global.yml`, whitelist), `internal/docker`
(minimal Docker API client), `internal/power` (shared state directory), `internal/settings`
(reloadable settings, persistent state), `internal/console`, `internal/idle`, `main.go`.
The version is defined only in the `VERSION` file.

## License

[MIT](LICENSE) © JonesJugHead
