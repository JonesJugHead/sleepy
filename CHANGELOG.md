# Changelog

Versions follow `v0.MINOR.PATCH`: PATCH increases with every release, and rolls over to the next MINOR instead of reaching 10.

## v0.1.0 — 2026-10-27

Sleepy is no longer tied to Pterodactyl: it can run in front of a server in its own Docker container.
The configuration changed and is not compatible with v0.0.x: start once to get the new template.

- `backend.type`: `"process"` (Sleepy runs the server command: Pterodactyl, systemd...) or
  `"docker"` (Sleepy starts and stops an existing container through the Docker API, over the socket
  or a docker-socket-proxy). Detected when `sleepy.toml` is first written.
- `backend.address` replaces `backend.port`: the server can be anywhere, e.g. `mc:25566` on a
  Docker network.
- Docker: a container started outside Sleepy (panel, `docker start`) is taken over and put to sleep
  like any other; one stopped outside Sleepy is noticed. Stops are `docker stop --timeout
  <stop_timeout>`. The server's files are never written. Console lines go to the container's stdin.
  When Sleepy itself stops, the container is left as it is.
- Long starts: 1.13+ players are kept waiting for as long as `hold_timeout` (now 180 s by default)
  instead of being asked to reconnect after 25 s, by sending them a login plugin request every
  10 s. Needed by modded servers (NeoForge, Arclight...) that take minutes to start.
- `[power]`: optional state directory shared with a panel and backup scripts. `admin.json`
  (`"stopped"`: never wake), `backup.lock` / `restore.lock` (do not start, with their own messages),
  `keep-awake` (no idle stop before a date), and `sleepy.json` written by Sleepy (state + heartbeat).
  New `motd.stopped`.
- `busy_command`, `busy_retry`, `busy_max_delay`: postpone an idle stop while a command says the
  server is busy (a map render...).
- The server icon and max players are remembered from the real server's status, like its MOTD, so
  they show while it sleeps even without access to its files. The MOTD is now picked up within
  seconds of the first start instead of after a minute.
- `TZ` is honoured even in images without time zone data.
- Docker image (`Dockerfile`) and a complete example in `examples/`.
- Default messages (`kick_message`, `motd.sleeping`, `motd.starting`) and the config generator are now in English.

## v0.0.4 — 2026-09-30

- `motd.source = "server"`: the server list shows the real server MOTD, even while asleep. Sleepy
  reads it while the server runs (at startup, then every minute), with its formatting, and stores it
  in `sleepy-state.json`. Before the first start, it uses `motd=` from `server.properties`.
  Default: `"sleepy"`, unchanged.
- `tools/config-generator.html`: offline `sleepy.toml` generator (form, in-game MOTD preview,
  validation, import, download).

## v0.0.3 — 2026-09-30

- Forwards the players' real IP with PROXY protocol v2 (`backend.proxy_protocol`, `auto` by
  default): on Paper and its forks, Sleepy enables `proxies.proxy-protocol` in
  `config/paper-global.yml` before each start. Logs, `/ban-ip` and plugins see the real IP instead
  of `127.0.0.1`.
- Console commands `sleepy status|on|off|sleep|wake|reload|help`, never forwarded to the server,
  even while Java runs. `sleepy off` disables auto-sleep persistently (`sleepy-state.json`).
- `sleepy reload`: re-reads `sleepy.toml` without restarting. Java process settings
  (command, port, directory) apply on the next start.

## v0.0.2 — 2026-09-30

- Rewritten in Go. Binary 77 MB → 3.4 MB, idle RAM 23 MB → 4 MB. Same behavior, same configuration.
- Static binary: no glibc dependency anymore, runs on any x86-64 CPU.
- The version is now defined in the `VERSION` file.
- Default Java command: `-XX:MaxRAMPercentage=85.0` instead of a fixed `-Xmx`, to follow the
  Pterodactyl server memory limit.
- README: startup command `./sleepy` (yolks images do not run compound commands).

## v0.0.1 — 2026-09-30

First release.

- Listens on the public port and keeps the Minecraft server off while nobody plays.
- Answers server-list pings while the server sleeps or starts: configurable MOTD, `version.protocol` = the client's protocol (never shown as incompatible), players `0/max-players`, favicon from `server-icon.png`. A ping never wakes the server.
- Answers the legacy `0xFE` ping (Beta 1.8 → 1.6).
- Wakes up when a player connects (login and transfer intents), holds the client until `hold_timeout`, then relays transparently (handshake and Login Start replayed). Otherwise, Login Disconnect with `kick_message`.
- `whitelist_only` option: only players in `whitelist.json` can wake the server.
- Rewrites `server-port` / `server-ip` in `server.properties` before each start.
- Detects startup from the `Done (` line or a local status ping, whichever comes first.
- Stops after `sleep_after` seconds without players (respecting `minimum_online_time`): `stop`, then SIGTERM after `stop_timeout`, then SIGKILL.
- Transparent Pterodactyl console while Java runs; `start`, `stop`, `status`, `help` commands while asleep.
- Clean shutdown on SIGINT / SIGTERM, exit code 0.
- Writes a commented `sleepy.toml` on first launch.
