package protocol

import (
	"encoding/hex"
	"net"
	"testing"
)

func TestProxyHeaderV2IPv4(t *testing.T) {
	src := &net.TCPAddr{IP: net.ParseIP("203.0.113.94"), Port: 29070}
	dst := &net.TCPAddr{IP: net.ParseIP("172.18.0.2"), Port: 25565}
	want := "0d0a0d0a000d0a515549540a" + // signature
		"21" + "11" + "000c" + // v2 PROXY, TCP/IPv4, 12 bytes
		"cb00715e" + "ac120002" + // 203.0.113.94, 172.18.0.2
		"718e" + "63dd" // 29070, 25565
	if got := hex.EncodeToString(ProxyHeaderV2(src, dst)); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestProxyHeaderV2IPv6(t *testing.T) {
	src := &net.TCPAddr{IP: net.ParseIP("2001:db8::1"), Port: 1}
	dst := &net.TCPAddr{IP: net.ParseIP("10.0.0.1"), Port: 2} // mixed: both sent as IPv6
	h := ProxyHeaderV2(src, dst)
	if len(h) != 16+36 || h[12] != 0x21 || h[13] != 0x21 || h[15] != 36 {
		t.Fatalf("header % x", h)
	}
	if got := net.IP(h[16:32]).String(); got != "2001:db8::1" {
		t.Errorf("source %s", got)
	}
	if got := net.IP(h[32:48]).String(); got != "10.0.0.1" {
		t.Errorf("destination %s", got)
	}
}

func TestProxyHeaderV2Local(t *testing.T) {
	if got := hex.EncodeToString(ProxyHeaderV2(nil, nil)); got != "0d0a0d0a000d0a515549540a"+"20000000" {
		t.Errorf("got %s", got)
	}
}
