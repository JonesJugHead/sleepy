package backend

import (
	"encoding/json"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"sleepy/internal/logx"
	"sleepy/internal/protocol"
)

// donePattern is printed by vanilla, Spigot, Paper, Fabric, Forge... once the server accepts players.
var donePattern = regexp.MustCompile(`Done \(`)

// lineWatcher splits a byte stream into lines and reports once when a line matches.
type lineWatcher struct {
	partial string
	matched bool
}

func (w *lineWatcher) feed(chunk []byte) bool {
	if w.matched {
		return false
	}
	lines := strings.Split(w.partial+string(chunk), "\n")
	w.partial = lines[len(lines)-1]
	if len(w.partial) > 4096 { // a server printing without newlines cannot grow this forever
		w.partial = w.partial[len(w.partial)-4096:]
	}
	for _, line := range lines[:len(lines)-1] {
		if donePattern.MatchString(line) {
			w.matched = true
			return true
		}
	}
	return false
}

// PingStatus asks the local server for its status and reports whether it answered validly.
func PingStatus(port int, proxyHeader bool, timeout time.Duration) bool {
	_, ok := FetchStatus(port, proxyHeader, timeout)
	return ok
}

// FetchStatus returns the local server's status JSON.
// Refused or closed connections are normal while the server boots and are not logged;
// a response that cannot be decoded is. proxyHeader must match what the server expects.
func FetchStatus(port int, proxyHeader bool, timeout time.Duration) (string, bool) {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), timeout)
	if err != nil {
		return "", false
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(timeout))
	var request []byte
	if proxyHeader {
		request = protocol.ProxyHeaderV2(conn.LocalAddr(), conn.RemoteAddr())
	}
	request = append(request, protocol.EncodeHandshake(protocol.Handshake{ProtocolVersion: -1, Address: "127.0.0.1", Port: uint16(port), Intent: protocol.IntentStatus})...)
	if _, err := conn.Write(append(request, protocol.EncodeStatusRequest()...)); err != nil {
		return "", false
	}
	reader := protocol.NewReader(1 << 20)
	buf := make([]byte, 4096)
	for {
		n, err := conn.Read(buf)
		reader.Push(buf[:n])
		packet, perr := reader.Next()
		if perr == nil && packet != nil {
			var js string
			if js, perr = protocol.DecodeStatusResponse(packet); perr == nil {
				if json.Valid([]byte(js)) {
					return js, true
				}
				logx.Printf("Invalid status response from the local server: not valid JSON")
				return "", false
			}
		}
		if perr != nil {
			logx.Printf("Invalid status response from the local server: %v", perr)
			return "", false
		}
		if err != nil {
			return "", false
		}
	}
}

// statusDescription extracts the MOTD (a JSON text component) from a status response.
func statusDescription(status string) json.RawMessage {
	var s struct {
		Description json.RawMessage `json:"description"`
	}
	if json.Unmarshal([]byte(status), &s) != nil || !json.Valid(s.Description) {
		return nil
	}
	return s.Description
}
