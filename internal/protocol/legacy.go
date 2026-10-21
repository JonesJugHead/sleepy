package protocol

// Server list ping of pre-1.7 clients. The request starts with 0xFE:
//   Beta 1.8 - 1.3 : FE                         -> "motd§online§max"
//   1.4 - 1.5      : FE 01                      -> "§1\0protocol\0version\0motd\0online\0max"
//   1.6            : FE 01 FA "MC|PingHost" ... -> same as 1.4, and the client sends its protocol
// The answer is a 0xFF kick packet carrying a UTF-16BE string.

import (
	"encoding/binary"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"
)

const LegacyPingByte = 0xfe

// fallbackProtocol is sent when a 1.4/1.5 client did not tell us its protocol.
const fallbackProtocol = 127

// v16ProtocolOffset: FE 01 FA, short, "MC|PingHost" (22 bytes), short, then the protocol byte.
const v16ProtocolOffset = 29

type LegacyPing struct {
	// Pre14 selects the Beta 1.8 - 1.3 response format.
	Pre14 bool
	// Protocol is the client protocol (1.6 only), -1 when unknown.
	Protocol int
}

// ClassifyLegacyPing identifies the ping flavour from the bytes received so far (buf[0] is 0xFE).
// ok is false while more bytes could still change the answer, unless final is set.
func ClassifyLegacyPing(buf []byte, final bool) (ping LegacyPing, ok bool) {
	unknown := LegacyPing{Protocol: -1}
	switch {
	case len(buf) < 2:
		return LegacyPing{Pre14: true, Protocol: -1}, final
	case buf[1] != 0x01:
		return LegacyPing{Pre14: true, Protocol: -1}, true
	case len(buf) < 3:
		return unknown, final
	case buf[2] != 0xfa:
		return unknown, true
	case len(buf) > v16ProtocolOffset:
		return LegacyPing{Protocol: int(buf[v16ProtocolOffset])}, true
	default:
		return unknown, final
	}
}

type LegacyStatus struct {
	Motd        string
	VersionName string
	Online      int
	Max         int
}

var colorCode = regexp.MustCompile(`§.?`)

func EncodeLegacyResponse(ping LegacyPing, s LegacyStatus) []byte {
	motd := strings.NewReplacer("\r\n", " ", "\n", " ").Replace(s.Motd)
	var text string
	if ping.Pre14 {
		// § is the field separator here, so color codes cannot be kept.
		text = colorCode.ReplaceAllString(motd, "") + "§" + strconv.Itoa(s.Online) + "§" + strconv.Itoa(s.Max)
	} else {
		protocol := ping.Protocol
		if protocol < 0 {
			protocol = fallbackProtocol
		}
		text = strings.Join([]string{"§1", strconv.Itoa(protocol), s.VersionName, motd, strconv.Itoa(s.Online), strconv.Itoa(s.Max)}, "\x00")
	}
	units := utf16.Encode([]rune(text))
	out := binary.BigEndian.AppendUint16([]byte{0xff}, uint16(len(units))) // length in UTF-16 code units
	for _, u := range units {
		out = binary.BigEndian.AppendUint16(out, u)
	}
	return out
}
