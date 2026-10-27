# Sleepy as a container, for servers that run in Docker (backend.type = "docker").
# Alpine rather than scratch: busy_command runs through sh.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# timetzdata: TZ works without the system time zone database (sleepy.json dates).
RUN CGO_ENABLED=0 go build -trimpath -tags timetzdata -ldflags='-s -w' -o /sleepy .

FROM alpine:3.22
COPY --from=build /sleepy /usr/local/bin/sleepy
# sleepy.toml and sleepy-state.json live here: mount a directory owned by the container user.
WORKDIR /data
EXPOSE 25565
ENTRYPOINT ["sleepy"]
