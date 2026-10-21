// Package protocol decodes the few Minecraft packets Sleepy needs before relaying raw bytes.
package protocol

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// Error is returned for any malformed or unexpected data received from a peer.
type Error struct{ msg string }

func (e *Error) Error() string { return e.msg }

func errorf(format string, args ...any) error { return &Error{fmt.Sprintf(format, args...)} }

// Errorf builds a protocol Error for callers outside this package.
func Errorf(format string, args ...any) error { return errorf(format, args...) }

// errIncomplete means the buffer ends before the value does: wait for more bytes.
var errIncomplete = errors.New("incomplete")

// AppendVarInt appends a 32-bit signed integer as a VarInt (negatives take 5 bytes).
func AppendVarInt(b []byte, value int32) []byte {
	v := uint32(value)
	for {
		if v < 0x80 {
			return append(b, byte(v))
		}
		b = append(b, byte(v&0x7f)|0x80)
		v >>= 7
	}
}

// DecodeVarInt decodes a VarInt at the start of buf. It returns errIncomplete when buf ends
// before the VarInt does.
func DecodeVarInt(buf []byte) (value int32, size int, err error) {
	var v uint32
	for i := 0; i < 5; i++ {
		if i >= len(buf) {
			return 0, 0, errIncomplete
		}
		v |= uint32(buf[i]&0x7f) << (7 * i)
		if buf[i]&0x80 == 0 {
			return int32(v), i + 1, nil
		}
	}
	return 0, 0, errorf("VarInt is longer than 5 bytes")
}

// AppendString appends a Minecraft String: VarInt byte length then UTF-8 bytes.
func AppendString(b []byte, s string) []byte {
	return append(AppendVarInt(b, int32(len(s))), s...)
}

// Cursor reads fields sequentially from one complete packet payload.
type Cursor struct {
	buf []byte
	pos int
}

func NewCursor(buf []byte) *Cursor { return &Cursor{buf: buf} }

func (c *Cursor) Remaining() int { return len(c.buf) - c.pos }

func (c *Cursor) ReadVarInt() (int32, error) {
	v, n, err := DecodeVarInt(c.buf[c.pos:])
	if err == errIncomplete {
		return 0, errorf("truncated VarInt")
	}
	c.pos += n
	return v, err
}

func (c *Cursor) ReadString(maxBytes int) (string, error) {
	n, err := c.ReadVarInt()
	if err != nil {
		return "", err
	}
	if n < 0 || int(n) > maxBytes {
		return "", errorf("invalid string length %d", n)
	}
	b, err := c.ReadBytes(int(n))
	return string(b), err
}

func (c *Cursor) ReadUShort() (uint16, error) {
	b, err := c.ReadBytes(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

func (c *Cursor) ReadBytes(n int) ([]byte, error) {
	if n > c.Remaining() {
		return nil, errorf("truncated packet: needed %d bytes, had %d", n, c.Remaining())
	}
	b := c.buf[c.pos : c.pos+n]
	c.pos += n
	return b, nil
}
