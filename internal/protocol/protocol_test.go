package protocol

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf16"
)

func isProtocolError(err error) bool {
	var pe *Error
	return errors.As(err, &pe)
}

// Reference vectors from https://minecraft.wiki/w/Java_Edition_protocol/Data_types#VarInt_and_VarLong
var varIntVectors = []struct {
	value int32
	hex   string
}{
	{0, "00"}, {1, "01"}, {2, "02"}, {127, "7f"}, {128, "8001"}, {255, "ff01"}, {25565, "ddc701"},
	{2097151, "ffff7f"}, {2147483647, "ffffffff07"}, {-1, "ffffffff0f"}, {-2147483648, "8080808008"},
}

func TestVarInt(t *testing.T) {
	for _, v := range varIntVectors {
		if got := hex.EncodeToString(AppendVarInt(nil, v.value)); got != v.hex {
			t.Errorf("encode %d = %s, want %s", v.value, got, v.hex)
		}
		b, _ := hex.DecodeString(v.hex)
		value, size, err := DecodeVarInt(b)
		if err != nil || value != v.value || size != len(b) {
			t.Errorf("decode %s = %d (%d bytes, %v), want %d", v.hex, value, size, err, v.value)
		}
	}
}

func TestVarIntIncompleteAndTooLong(t *testing.T) {
	for _, h := range []string{"", "ddc7"} {
		b, _ := hex.DecodeString(h)
		if _, _, err := DecodeVarInt(b); err != errIncomplete {
			t.Errorf("%q: got %v, want errIncomplete", h, err)
		}
	}
	if _, _, err := DecodeVarInt([]byte{0xff, 0xff, 0xff, 0xff, 0xff, 0x01}); !isProtocolError(err) {
		t.Errorf("6-byte VarInt: got %v", err)
	}
}

func TestStringAndCursor(t *testing.T) {
	s := "§aHé ✓"
	enc := AppendString(nil, s)
	if got, err := NewCursor(enc).ReadString(100); err != nil || got != s || int(enc[0]) != len(s) {
		t.Fatalf("round trip: %q %v", got, err)
	}
	if _, err := NewCursor(enc[:4]).ReadString(100); !isProtocolError(err) {
		t.Errorf("truncated string: %v", err)
	}
	if _, err := NewCursor([]byte{0x01}).ReadUShort(); !isProtocolError(err) {
		t.Errorf("truncated ushort: %v", err)
	}
	if _, err := NewCursor([]byte{0x80}).ReadVarInt(); !isProtocolError(err) {
		t.Errorf("truncated varint: %v", err)
	}
	if _, err := NewCursor(AppendString(nil, "toolong")).ReadString(3); !isProtocolError(err) {
		t.Errorf("max length: %v", err)
	}
}

var testHandshake = Handshake{ProtocolVersion: 767, Address: "play.example.com", Port: 25565, Intent: 2}

func readOne(t *testing.T, b []byte) *RawPacket {
	t.Helper()
	r := NewReader(1 << 20)
	r.Push(b)
	p, err := r.Next()
	if err != nil || p == nil {
		t.Fatalf("readOne: %v %v", p, err)
	}
	if len(r.Buffered()) != 0 {
		t.Fatalf("leftover bytes")
	}
	return p
}

func TestReaderByteByByte(t *testing.T) {
	frame := EncodeHandshake(testHandshake)
	r := NewReader(DefaultMaxLength)
	for i := 0; i < len(frame)-1; i++ {
		r.Push(frame[i : i+1])
		if p, err := r.Next(); p != nil || err != nil {
			t.Fatalf("early packet at %d: %v %v", i, p, err)
		}
	}
	r.Push(frame[len(frame)-1:])
	p, err := r.Next()
	if err != nil || p.ID != 0 || !bytes.Equal(p.Raw, frame) {
		t.Fatalf("got %v %v", p, err)
	}
}

func TestReaderSeveralPacketsAndDrain(t *testing.T) {
	hs, login := EncodeHandshake(testHandshake), EncodeLoginStart("Steve")
	r := NewReader(DefaultMaxLength)
	r.Push(bytes.Join([][]byte{hs, login, {0x05, 0x01}}, nil))
	if p, _ := r.Next(); !bytes.Equal(p.Raw, hs) {
		t.Fatal("first packet")
	}
	if p, _ := r.Next(); !bytes.Equal(p.Raw, login) {
		t.Fatal("second packet")
	}
	if p, err := r.Next(); p != nil || err != nil {
		t.Fatal("incomplete third packet should wait")
	}
	if rest := hex.EncodeToString(r.Drain()); rest != "0501" || len(r.Buffered()) != 0 {
		t.Fatalf("drain = %s", rest)
	}
}

func TestReaderRawNotClobberedByLaterPush(t *testing.T) {
	r := NewReader(DefaultMaxLength)
	first := EncodeLoginStart("Steve")
	r.Push(append(append([]byte{}, first...), 0x02))
	p, _ := r.Next()
	r.Push(bytes.Repeat([]byte{0xaa}, 64))
	if !bytes.Equal(p.Raw, first) {
		t.Fatal("raw packet modified by a later Push")
	}
}

func TestReaderRejectsBadLengths(t *testing.T) {
	for _, b := range [][]byte{{0x00}, AppendVarInt(nil, -1), AppendVarInt(nil, 1<<20)} {
		r := NewReader(DefaultMaxLength)
		r.Push(b)
		if _, err := r.Next(); !isProtocolError(err) {
			t.Errorf("% x: got %v", b, err)
		}
	}
}

func TestHandshake(t *testing.T) {
	for _, pv := range []int32{4, 47, 340, 769, 99999, -1} {
		h := testHandshake
		h.ProtocolVersion = pv
		got, err := DecodeHandshake(readOne(t, EncodeHandshake(h)))
		if err != nil || got != h {
			t.Errorf("protocol %d: %+v %v", pv, got, err)
		}
	}
	long := testHandshake
	long.Address = "example.com\x00FML3\x00" + strings.Repeat("x", 2000)
	if got, _ := DecodeHandshake(readOne(t, EncodeHandshake(long))); got.Address != long.Address {
		t.Error("long forwarding address lost")
	}
	if _, err := DecodeHandshake(readOne(t, EncodePacket(0x01))); !isProtocolError(err) {
		t.Error("wrong id accepted")
	}
	if _, err := DecodeHandshake(readOne(t, EncodePacket(0x00, AppendVarInt(nil, 767)))); !isProtocolError(err) {
		t.Error("truncated handshake accepted")
	}
}

func TestLoginStart(t *testing.T) {
	if name, err := DecodeLoginStart(readOne(t, EncodeLoginStart("Steve"))); name != "Steve" || err != nil {
		t.Errorf("1.7-1.18 format: %q %v", name, err)
	}
	// 1.20.2+ adds a UUID, 1.19 a signature: ignored.
	withUUID := EncodePacket(0x00, AppendString(nil, "Alex"), bytes.Repeat([]byte{0xab}, 16))
	withSig := EncodePacket(0x00, AppendString(nil, "Alex"), []byte{0x01}, bytes.Repeat([]byte{7}, 300))
	for _, b := range [][]byte{withUUID, withSig} {
		if name, err := DecodeLoginStart(readOne(t, b)); name != "Alex" || err != nil {
			t.Errorf("extra fields: %q %v", name, err)
		}
	}
	if _, err := DecodeLoginStart(readOne(t, EncodeLoginStart(""))); !isProtocolError(err) {
		t.Error("empty username accepted")
	}
}

func TestStatus(t *testing.T) {
	if got := hex.EncodeToString(EncodeStatusRequest()); got != "0100" {
		t.Errorf("status request = %s", got)
	}
	js := BuildStatusJSON(StatusInfo{VersionName: "Sleepy", Protocol: 767, Motd: "§7Zzz", Max: 20})
	if got, err := DecodeStatusResponse(readOne(t, EncodeStatusResponse(js))); got != js || err != nil {
		t.Fatalf("status response round trip: %v", err)
	}
	var parsed, want any
	json.Unmarshal([]byte(js), &parsed)
	json.Unmarshal([]byte(`{"version":{"name":"Sleepy","protocol":767},"players":{"max":20,"online":0,"sample":[]},"description":{"text":"§7Zzz"}}`), &want)
	if !reflect.DeepEqual(parsed, want) {
		t.Errorf("status JSON = %s", js)
	}
	withIcon := BuildStatusJSON(StatusInfo{Favicon: "data:image/png;base64,AA=="})
	if !strings.Contains(withIcon, `"favicon":"data:image/png;base64,AA=="`) {
		t.Errorf("favicon missing: %s", withIcon)
	}
	payload, _ := hex.DecodeString("0102030405060708")
	if p := readOne(t, EncodePong(payload)); p.ID != PingID || !bytes.Equal(p.Data, payload) {
		t.Error("pong does not echo the payload")
	}
}

func TestLoginDisconnect(t *testing.T) {
	p := readOne(t, EncodeLoginDisconnect(`Le serveur démarre "vite" <ok>`))
	s, _ := NewCursor(p.Data).ReadString(1000)
	var got map[string]string
	if err := json.Unmarshal([]byte(s), &got); err != nil || p.ID != 0 || got["text"] != `Le serveur démarre "vite" <ok>` {
		t.Errorf("disconnect = %s", s)
	}
}

// ping16 builds the exact bytes a 1.6 client sends.
func ping16(protocol byte) []byte {
	utf16be := func(s string) []byte {
		var b []byte
		for _, u := range utf16.Encode([]rune(s)) {
			b = binary.BigEndian.AppendUint16(b, u)
		}
		return b
	}
	host := utf16be("localhost")
	rest := append(append(append([]byte{protocol}, 0, 9), host...), 0, 0, 0x63, 0xdd)
	b := append([]byte{0xfe, 0x01, 0xfa, 0, 11}, utf16be("MC|PingHost")...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(rest)))
	return append(b, rest...)
}

func decodeLegacy(t *testing.T, b []byte) string {
	t.Helper()
	if b[0] != 0xff {
		t.Fatal("not a kick packet")
	}
	n := int(binary.BigEndian.Uint16(b[1:3]))
	units := make([]uint16, 0, n)
	for i := 3; i+1 < len(b); i += 2 {
		units = append(units, binary.BigEndian.Uint16(b[i:]))
	}
	if len(units) != n {
		t.Fatalf("length %d, got %d units", n, len(units))
	}
	return string(utf16.Decode(units))
}

func TestClassifyLegacyPing(t *testing.T) {
	cases := []struct {
		buf    []byte
		final  bool
		want   LegacyPing
		wantOK bool
	}{
		{[]byte{0xfe}, false, LegacyPing{Pre14: true, Protocol: -1}, false},
		{[]byte{0xfe}, true, LegacyPing{Pre14: true, Protocol: -1}, true},
		{[]byte{0xfe, 0x01}, false, LegacyPing{Protocol: -1}, false},
		{[]byte{0xfe, 0x01}, true, LegacyPing{Protocol: -1}, true},
		{ping16(78)[:10], false, LegacyPing{Protocol: -1}, false},
		{ping16(78), false, LegacyPing{Protocol: 78}, true},
	}
	for i, c := range cases {
		got, ok := ClassifyLegacyPing(c.buf, c.final)
		if ok != c.wantOK || (ok && got != c.want) {
			t.Errorf("case %d: got %+v %v", i, got, ok)
		}
	}
}

func TestLegacyResponse(t *testing.T) {
	s := LegacyStatus{Motd: "§aHello\nworld", VersionName: "Sleepy", Online: 0, Max: 20}
	if got := strings.Split(decodeLegacy(t, EncodeLegacyResponse(LegacyPing{Protocol: 78}, s)), "\x00"); !reflect.DeepEqual(got, []string{"§1", "78", "Sleepy", "§aHello world", "0", "20"}) {
		t.Errorf("1.4+ format: %q", got)
	}
	if got := strings.Split(decodeLegacy(t, EncodeLegacyResponse(LegacyPing{Protocol: -1}, s)), "\x00")[1]; got != "127" {
		t.Errorf("fallback protocol: %s", got)
	}
	if got := decodeLegacy(t, EncodeLegacyResponse(LegacyPing{Pre14: true, Protocol: -1}, s)); got != "Hello world§0§20" {
		t.Errorf("pre-1.4 format: %q", got)
	}
}

func TestStatusWithServerDescription(t *testing.T) {
	desc := json.RawMessage(`{"text":"","extra":[{"text":"Hello ","color":"#ff8800"},{"text":"world","bold":true}]}`)
	js := BuildStatusJSON(StatusInfo{VersionName: "S", Protocol: 5, Motd: "ignored", Max: 1, Description: desc})
	if !strings.Contains(js, `"description":`+string(desc)) {
		t.Errorf("description not used as is: %s", js)
	}
	if got := BuildStatusJSON(StatusInfo{Motd: "fallback", Description: json.RawMessage(`{broken`)}); !strings.Contains(got, `"description":{"text":"fallback"}`) {
		t.Errorf("invalid description not replaced: %s", got)
	}
}

func TestPlainText(t *testing.T) {
	cases := map[string]string{
		`"plain"`: "plain",
		`{"text":"a","extra":[{"text":"b"},"c",{"text":"d","extra":[{"text":"e"}]}]}`: "abcde",
		`[{"text":"x"},{"text":"y"}]`: "xy",
		`{broken`:                     "",
	}
	for in, want := range cases {
		if got := PlainText(json.RawMessage(in)); got != want {
			t.Errorf("PlainText(%s) = %q, want %q", in, got, want)
		}
	}
}
