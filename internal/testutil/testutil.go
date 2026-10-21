// Package testutil provides a fake Minecraft server and helpers for integration tests.
//
// The fake server is the test binary itself, re-executed with SLEEPY_FAKE_SERVER=1: packages
// using it call MaybeRunFakeServer from TestMain. It is driven by environment variables:
//
//	FAKE_DELAY        ms before listening (default 200)
//	FAKE_NO_DONE      never print the "Done (" line (readiness must come from the status ping)
//	FAKE_IGNORE_STOP  ignore the "stop" console command
//	FAKE_CRASH_AFTER  exit with code 3 after this many ms
package testutil

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"sleepy/internal/protocol"
)

const envFlag = "SLEEPY_FAKE_SERVER"

// MaybeRunFakeServer turns the process into the fake server when asked to, and never returns then.
func MaybeRunFakeServer() {
	if os.Getenv(envFlag) != "1" {
		return
	}
	runFakeServer()
	os.Exit(0)
}

// FakeServerCommand returns the backend command running the fake server with the given variables.
func FakeServerCommand(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(envFlag + "=1 ")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s ", k, env[k])
	}
	fmt.Fprintf(&b, "'%s' -test.run=^$", os.Args[0])
	return b.String()
}

func TempServerDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		os.MkdirAll(filepath.Dir(path), 0o755)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func FreePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// Until polls cond until it is true or fails the test after timeout.
func Until(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func envMillis(name string, def int) time.Duration {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return time.Duration(v) * time.Millisecond
	}
	return time.Duration(def) * time.Millisecond
}

func runFakeServer() {
	props, _ := os.ReadFile("server.properties")
	port := regexp.MustCompile(`(?m)^server-port=(\d+)`).FindSubmatch(props)
	ipMatch := regexp.MustCompile(`(?m)^server-ip=(.*?)\r?$`).FindSubmatch(props)
	ip := "0.0.0.0"
	if ipMatch != nil && len(ipMatch[1]) > 0 {
		ip = string(ipMatch[1])
	}
	if port == nil {
		fmt.Println("[fake] no server-port in server.properties")
		os.Exit(1)
	}
	fmt.Printf("[fake] Starting minecraft server on %s:%s\n", ip, port[1])

	if d := envMillis("FAKE_CRASH_AFTER", 0); d > 0 {
		time.AfterFunc(d, func() { os.Exit(3) })
	}
	go func() {
		time.Sleep(envMillis("FAKE_DELAY", 200))
		ln, err := net.Listen("tcp", net.JoinHostPort(ip, string(port[1])))
		if err != nil {
			fmt.Println("[fake] cannot listen:", err)
			os.Exit(1)
		}
		if os.Getenv("FAKE_NO_DONE") == "" {
			fmt.Println(`[fake] Done (0.2s)! For help, type "help"`)
		}
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go fakeHandle(c, ip)
		}
	}()

	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "stop" && os.Getenv("FAKE_IGNORE_STOP") == "" {
			fmt.Println("[fake] Stopping the server")
			time.Sleep(50 * time.Millisecond)
			os.Exit(0)
		}
		fmt.Println("[fake] console:", line)
	}
	select {}
}

// expectsProxyProtocol mimics Paper: the PROXY header is required when paper-global.yml says so.
func expectsProxyProtocol() bool {
	content, _ := os.ReadFile("config/paper-global.yml")
	return regexp.MustCompile(`(?m)^\s+proxy-protocol: true`).Match(content)
}

// readProxyHeader reads a PROXY v2 header and returns the source IP, like Paper would.
func readProxyHeader(c net.Conn) (string, []byte, error) {
	var buf []byte
	chunk := make([]byte, 4096)
	for len(buf) < 16 || len(buf) < 16+int(binary.BigEndian.Uint16(buf[14:16])) {
		if len(buf) >= 12 && !bytes.Equal(buf[:12], protocol.ProxyV2Signature) {
			return "", nil, fmt.Errorf("no PROXY header")
		}
		n, err := c.Read(chunk)
		if err != nil {
			return "", nil, err
		}
		buf = append(buf, chunk[:n]...)
	}
	end := 16 + int(binary.BigEndian.Uint16(buf[14:16]))
	switch buf[13] {
	case 0x11:
		return net.IP(buf[16:20]).String(), buf[end:], nil
	case 0x21:
		return net.IP(buf[16:32]).String(), buf[end:], nil
	}
	return "local", buf[end:], nil
}

func fakeHandle(c net.Conn, ip string) {
	defer c.Close()
	r := protocol.NewReader(protocol.DefaultMaxLength)
	buf := make([]byte, 4096)
	var hs *protocol.Handshake
	from := ""
	if expectsProxyProtocol() {
		src, rest, err := readProxyHeader(c)
		if err != nil {
			fmt.Println("[fake] rejected connection:", err)
			return
		}
		from = " from " + src
		r.Push(rest)
	}
	// Handle every complete packet already buffered (bytes that followed the PROXY header), then read.
	for {
		for {
			p, perr := r.Next()
			if perr != nil {
				return
			}
			if p == nil {
				break
			}
			switch {
			case hs == nil:
				h, _ := protocol.DecodeHandshake(p)
				hs = &h
			case hs.Intent == protocol.IntentStatus && p.ID == protocol.StatusRequestID:
				js := protocol.BuildStatusJSON(protocol.StatusInfo{VersionName: "Fake", Protocol: hs.ProtocolVersion, Motd: "fake", Online: 1, Max: 20})
				c.Write(protocol.EncodeStatusResponse(js))
			case hs.Intent == protocol.IntentStatus && p.ID == protocol.PingID:
				c.Write(protocol.EncodePong(p.Data))
				return
			default:
				// Login: proves the handshake and login start were replayed intact.
				name, _ := protocol.DecodeLoginStart(p)
				c.Write(protocol.EncodeLoginDisconnect("hello " + name + " via " + ip + from))
				return
			}
		}
		n, err := c.Read(buf)
		if err != nil {
			return
		}
		r.Push(buf[:n])
	}
}
