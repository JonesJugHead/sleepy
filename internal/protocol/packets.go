package protocol

// Only the fields that have been stable across every protocol version since 1.7 are parsed here.

import (
	"encoding/binary"
	"encoding/json"
	"strings"
)

const (
	IntentStatus   = 1
	IntentLogin    = 2
	IntentTransfer = 3

	StatusRequestID = 0x00
	PingID          = 0x01
)

type Handshake struct {
	ProtocolVersion int32
	Address         string
	Port            uint16
	Intent          int32
}

// EncodePacket frames a packet: VarInt length, VarInt id, payload.
func EncodePacket(id int32, payload ...[]byte) []byte {
	body := AppendVarInt(nil, id)
	for _, p := range payload {
		body = append(body, p...)
	}
	return append(AppendVarInt(nil, int32(len(body))), body...)
}

func DecodeHandshake(p *RawPacket) (Handshake, error) {
	var h Handshake
	if p.ID != 0x00 {
		return h, errorf("expected handshake (0x00), got packet 0x%x", p.ID)
	}
	c := NewCursor(p.Data)
	var err error
	if h.ProtocolVersion, err = c.ReadVarInt(); err != nil {
		return h, err
	}
	// Proxies (BungeeCord/Velocity forwarding, Forge markers) can put long data in the address.
	if h.Address, err = c.ReadString(32767 * 3); err != nil {
		return h, err
	}
	if h.Port, err = c.ReadUShort(); err != nil {
		return h, err
	}
	h.Intent, err = c.ReadVarInt()
	return h, err
}

func EncodeHandshake(h Handshake) []byte {
	b := AppendVarInt(nil, h.ProtocolVersion)
	b = AppendString(b, h.Address)
	b = binary.BigEndian.AppendUint16(b, h.Port)
	return EncodePacket(0x00, AppendVarInt(b, h.Intent))
}

// DecodeLoginStart reads only the username: it is the first field of Login Start in every
// version, the rest varies.
func DecodeLoginStart(p *RawPacket) (string, error) {
	if p.ID != 0x00 {
		return "", errorf("expected login start (0x00), got packet 0x%x", p.ID)
	}
	// Lenient bound: prefixed (Floodgate) or offline-mode names are the real server's call, not ours.
	name, err := NewCursor(p.Data).ReadString(256)
	if err == nil && name == "" {
		err = errorf("empty username in login start")
	}
	return name, err
}

func EncodeLoginStart(username string) []byte { return EncodePacket(0x00, AppendString(nil, username)) }

func EncodeStatusRequest() []byte { return EncodePacket(StatusRequestID) }

func EncodeStatusResponse(json string) []byte { return EncodePacket(0x00, AppendString(nil, json)) }

func DecodeStatusResponse(p *RawPacket) (string, error) {
	if p.ID != 0x00 {
		return "", errorf("expected status response (0x00), got packet 0x%x", p.ID)
	}
	return NewCursor(p.Data).ReadString(32767 * 4)
}

// EncodePong echoes the ping payload untouched (a long in practice).
func EncodePong(payload []byte) []byte { return EncodePacket(PingID, payload) }

// EncodeLoginDisconnect builds a Login Disconnect: a JSON text component in every version
// (NBT is only used in later states).
func EncodeLoginDisconnect(message string) []byte {
	return EncodePacket(0x00, AppendString(nil, textComponent(message)))
}

// PlainText flattens a JSON text component (string, object with text/extra, or array) to its
// text, for legacy clients. Formatting is dropped.
func PlainText(component json.RawMessage) string {
	var v any
	if json.Unmarshal(component, &v) != nil {
		return ""
	}
	var b strings.Builder
	var walk func(any)
	walk = func(n any) {
		switch t := n.(type) {
		case string:
			b.WriteString(t)
		case []any:
			for _, e := range t {
				walk(e)
			}
		case map[string]any:
			if s, ok := t["text"].(string); ok {
				b.WriteString(s)
			}
			if extra, ok := t["extra"].([]any); ok {
				walk(extra)
			}
		}
	}
	walk(v)
	return b.String()
}

func textComponent(text string) string {
	b, _ := json.Marshal(struct {
		Text string `json:"text"`
	}{text})
	return string(b)
}

type StatusInfo struct {
	VersionName string
	// Protocol echoes the client's so the server never shows as incompatible.
	Protocol int32
	Motd     string
	Online   int
	Max      int
	// Favicon is a data:image/png;base64,... URL, omitted when empty.
	Favicon string
	// Description, if set, is a JSON text component used as is instead of Motd
	// (the real server's MOTD, with its formatting).
	Description json.RawMessage
}

func BuildStatusJSON(info StatusInfo) string {
	type version struct {
		Name     string `json:"name"`
		Protocol int32  `json:"protocol"`
	}
	type players struct {
		Max    int   `json:"max"`
		Online int   `json:"online"`
		Sample []any `json:"sample"`
	}
	description := json.RawMessage(textComponent(info.Motd))
	if len(info.Description) > 0 && json.Valid(info.Description) {
		description = info.Description
	}
	b, _ := json.Marshal(struct {
		Version     version         `json:"version"`
		Players     players         `json:"players"`
		Description json.RawMessage `json:"description"`
		Favicon     string          `json:"favicon,omitempty"`
	}{
		Version:     version{info.VersionName, info.Protocol},
		Players:     players{info.Max, info.Online, []any{}},
		Description: description,
		Favicon:     info.Favicon,
	})
	return string(b)
}

// Login plugin messages (1.13+, protocol 393): Sleepy sends requests on an unknown channel while a
// player waits, which keeps the client from timing out (it resets its 30 s read timeout on every
// packet). The client answers each one with "not understood".
const (
	LoginPluginMinProtocol = 393
	LoginPluginRequestID   = 0x04
	LoginPluginResponseID  = 0x02
)

func EncodeLoginPluginRequest(messageID int32, channel string) []byte {
	return EncodePacket(LoginPluginRequestID, AppendVarInt(nil, messageID), AppendString(nil, channel))
}

// DecodeLoginPluginResponse returns the message id of a login plugin response.
func DecodeLoginPluginResponse(p *RawPacket) (int32, error) {
	if p.ID != LoginPluginResponseID {
		return 0, errorf("not a login plugin response: packet 0x%x", p.ID)
	}
	return NewCursor(p.Data).ReadVarInt()
}

// EncodeLoginPluginResponse is what a client sends back (tests).
func EncodeLoginPluginResponse(messageID int32) []byte {
	return EncodePacket(LoginPluginResponseID, AppendVarInt(nil, messageID), []byte{0})
}
