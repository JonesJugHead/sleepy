package proxy

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"sleepy/internal/backend"
	"sleepy/internal/config"
	"sleepy/internal/logx"
	"sleepy/internal/protocol"
)

// Everything is read from disk on each ping: nothing stays in memory while the server sleeps.

func currentMotd(b *backend.Backend, motd config.Motd) string {
	if b.State() == backend.Starting {
		return motd.Starting
	}
	return motd.Sleeping
}

// serverMotd returns the real server's MOTD for motd.source = "server": the one remembered from
// its last run, else motd= from server.properties. ok is false when neither is available.
func serverMotd(b *backend.Backend) (description json.RawMessage, text string, ok bool) {
	if d := b.ServerDescription(); len(d) > 0 {
		return d, protocol.PlainText(d), true
	}
	if m := backend.ReadMotd(b.Directory()); m != "" {
		return nil, m, true
	}
	return nil, "", false
}

func localStatusJSON(b *backend.Backend, motd config.Motd, clientProtocol int32) string {
	info := protocol.StatusInfo{
		VersionName: motd.VersionName,
		Protocol:    clientProtocol,
		Motd:        currentMotd(b, motd),
		Max:         backend.ReadMaxPlayers(b.Directory()),
		Favicon:     readFavicon(b.Directory()),
	}
	if motd.Source == "server" {
		if description, text, ok := serverMotd(b); ok {
			info.Description, info.Motd = description, text
		}
	}
	return protocol.BuildStatusJSON(info)
}

func localLegacyStatus(b *backend.Backend, motd config.Motd) protocol.LegacyStatus {
	text := currentMotd(b, motd)
	if motd.Source == "server" {
		if _, t, ok := serverMotd(b); ok {
			text = t
		}
	}
	return protocol.LegacyStatus{
		Motd:        text,
		VersionName: motd.VersionName,
		Max:         backend.ReadMaxPlayers(b.Directory()),
	}
}

func readFavicon(directory string) string {
	path := filepath.Join(directory, "server-icon.png")
	png, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logx.Printf("Cannot read %s: %v", path, err)
		}
		return ""
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)
}

// serveStatus answers the status request and the ping of one server list refresh.
func serveStatus(c *clientConn, json string) error {
	for {
		p, err := c.next()
		if err != nil {
			return err
		}
		switch p.ID {
		case protocol.StatusRequestID:
			if _, err := c.Write(protocol.EncodeStatusResponse(json)); err != nil {
				return err
			}
		case protocol.PingID:
			_, err := c.Write(protocol.EncodePong(p.Data))
			return err
		default:
			return protocolErrorf("unexpected packet 0x%x in status state", p.ID)
		}
	}
}
