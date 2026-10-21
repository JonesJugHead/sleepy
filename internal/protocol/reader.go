package protocol

// RawPacket is one uncompressed, unencrypted packet.
type RawPacket struct {
	ID int32
	// Data is the payload after the packet id.
	Data []byte
	// Raw is the whole frame as received (length prefix included), replayed verbatim to the backend.
	Raw []byte
}

// DefaultMaxLength bounds packets read before the relay: the handshake and login start are small.
const DefaultMaxLength = 64 * 1024

// Reader reassembles packets from arbitrary TCP chunks. Only used before the relay starts,
// i.e. before compression or encryption can be enabled.
type Reader struct {
	buf       []byte
	maxLength int
}

func NewReader(maxLength int) *Reader { return &Reader{maxLength: maxLength} }

func (r *Reader) Push(chunk []byte) { r.buf = append(r.buf, chunk...) }

// Buffered returns the bytes received but not consumed yet.
func (r *Reader) Buffered() []byte { return r.buf }

// Next returns the next complete packet, or nil if more data is needed.
func (r *Reader) Next() (*RawPacket, error) {
	length, n, err := DecodeVarInt(r.buf)
	if err == errIncomplete {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if length <= 0 || int(length) > r.maxLength {
		return nil, errorf("invalid packet length %d", length)
	}
	end := n + int(length)
	if len(r.buf) < end {
		return nil, nil
	}
	raw := r.buf[:end:end] // capped so later appends to r.buf never touch it
	id, idSize, err := DecodeVarInt(raw[n:])
	if err != nil {
		return nil, errorf("packet id overruns packet length")
	}
	r.buf = r.buf[end:]
	return &RawPacket{ID: id, Data: raw[n+idSize:], Raw: raw}, nil
}

// Drain hands over whatever is left (to be replayed) and empties the reader.
func (r *Reader) Drain() []byte {
	rest := r.buf
	r.buf = nil
	return rest
}
