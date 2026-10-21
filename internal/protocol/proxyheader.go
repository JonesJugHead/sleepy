package protocol

import (
	"encoding/binary"
	"net"
)

// ProxyV2Signature starts every PROXY protocol v2 header.
// Spec: https://www.haproxy.org/download/2.9/doc/proxy-protocol.txt
var ProxyV2Signature = []byte("\r\n\r\n\x00\r\nQUIT\n")

// ProxyHeaderV2 builds the header telling the server that this connection comes from src
// (the player) to dst. Sent before any Minecraft byte on a relayed connection.
func ProxyHeaderV2(src, dst net.Addr) []byte {
	h := append([]byte{}, ProxyV2Signature...)
	s, sok := src.(*net.TCPAddr)
	d, dok := dst.(*net.TCPAddr)
	if !sok || !dok {
		return append(h, 0x20, 0x00, 0, 0) // LOCAL: no address information
	}
	if s4, d4 := s.IP.To4(), d.IP.To4(); s4 != nil && d4 != nil {
		h = append(h, 0x21, 0x11, 0, 12) // v2 PROXY, TCP over IPv4, 12 address bytes
		h = append(append(h, s4...), d4...)
	} else {
		h = append(h, 0x21, 0x21, 0, 36) // v2 PROXY, TCP over IPv6, 36 address bytes
		h = append(append(h, s.IP.To16()...), d.IP.To16()...)
	}
	h = binary.BigEndian.AppendUint16(h, uint16(s.Port))
	return binary.BigEndian.AppendUint16(h, uint16(d.Port))
}
