package docker

import (
	"bufio"
	"context"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

// serveOnce answers one request with a raw HTTP response and returns the request line.
func serveOnce(t *testing.T, response string) (*Client, <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	got := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		line, _ := bufio.NewReader(conn).ReadString('\n')
		got <- strings.TrimSpace(line)
		io.WriteString(conn, response)
	}()
	c, err := New("tcp://" + ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return c, got
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

func TestInspectChunked(t *testing.T) {
	body := `{"State":{"Running":true,"StartedAt":"2026-10-27T18:00:00Z","ExitCode":0,"Health":{"Status":"healthy"}},"Config":{"OpenStdin":true}}`
	chunked := "10\r\n" + body[:16] + "\r\n" + strconv.FormatInt(int64(len(body)-16), 16) + "\r\n" + body[16:] + "\r\n0\r\n\r\n"
	c, got := serveOnce(t, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n"+chunked)
	s, err := c.Inspect(ctx(t), "mc")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Running || s.StartedAt != "2026-10-27T18:00:00Z" || s.Health != "healthy" || !s.OpenStdin {
		t.Fatalf("state %+v", s)
	}
	if line := <-got; line != "GET /containers/mc/json HTTP/1.1" {
		t.Fatalf("request %q", line)
	}
}

func TestStatusCodes(t *testing.T) {
	c, got := serveOnce(t, "HTTP/1.1 304 Not Modified\r\nContent-Length: 0\r\n\r\n")
	if err := c.Start(ctx(t), "mc"); err != nil {
		t.Fatalf("304: %v", err)
	}
	if line := <-got; line != "POST /containers/mc/start HTTP/1.1" {
		t.Fatalf("request %q", line)
	}
	c, got = serveOnce(t, "HTTP/1.1 204 No Content\r\n\r\n")
	if err := c.Stop(ctx(t), "mc", 170*time.Second); err != nil {
		t.Fatalf("204: %v", err)
	}
	if line := <-got; line != "POST /containers/mc/stop?t=170 HTTP/1.1" {
		t.Fatalf("request %q", line)
	}
	msg := `{"message":"No such container: nope"}`
	c, _ = serveOnce(t, "HTTP/1.1 404 Not Found\r\nContent-Length: "+strconv.Itoa(len(msg))+"\r\n\r\n"+msg)
	if _, err := c.Inspect(ctx(t), "nope"); err != ErrNotFound {
		t.Fatalf("404: %v", err)
	}
	msg = `{"message":"forbidden by the proxy"}`
	c, _ = serveOnce(t, "HTTP/1.1 403 Forbidden\r\nContent-Length: "+strconv.Itoa(len(msg))+"\r\n\r\n"+msg)
	if err := c.Start(ctx(t), "mc"); err == nil || !strings.Contains(err.Error(), "forbidden by the proxy (HTTP 403)") {
		t.Fatalf("403: %v", err)
	}
}

func TestEndpoints(t *testing.T) {
	for endpoint, want := range map[string]string{
		"unix:///var/run/docker.sock": "unix:///var/run/docker.sock",
		"tcp://proxy:2375":            "tcp://proxy:2375",
		"http://127.0.0.1:2375":       "tcp://127.0.0.1:2375",
	} {
		c, err := New(endpoint)
		if err != nil || c.Endpoint() != want {
			t.Errorf("%s: %v %v", endpoint, c, err)
		}
	}
	if _, err := New("npipe:////./pipe/docker_engine"); err == nil {
		t.Error("npipe accepted")
	}
}
