// Package docker is a minimal Docker Engine API client: inspect, start and stop one container, and
// write to its console. Plain HTTP/1.1 over a unix socket or TCP, so it also works through a
// docker-socket-proxy. Paths carry no API version: the daemon uses its own.
//
// net/http is deliberately not used: it would double the size of the binary for four requests.
package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultSocket = "/var/run/docker.sock"

type Client struct {
	network, address string
}

// New connects to endpoint: "unix:///path", "tcp://host:port" or "http://host:port".
// Empty means $DOCKER_HOST, else the local socket.
func New(endpoint string) (*Client, error) {
	if endpoint == "" {
		endpoint = os.Getenv("DOCKER_HOST")
	}
	if endpoint == "" {
		endpoint = "unix://" + defaultSocket
	}
	switch {
	case strings.HasPrefix(endpoint, "unix://"):
		return &Client{network: "unix", address: strings.TrimPrefix(endpoint, "unix://")}, nil
	case strings.HasPrefix(endpoint, "tcp://"), strings.HasPrefix(endpoint, "http://"):
		u, err := url.Parse(endpoint)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("invalid Docker endpoint %q", endpoint)
		}
		return &Client{network: "tcp", address: u.Host}, nil
	}
	return nil, fmt.Errorf("unsupported Docker endpoint %q (use unix://, tcp:// or http://)", endpoint)
}

// Endpoint describes where the client connects, for logs.
func (c *Client) Endpoint() string { return c.network + "://" + c.address }

// ErrNotFound: no container with that name.
var ErrNotFound = errors.New("no such container")

type State struct {
	Running   bool
	StartedAt string
	ExitCode  int
	// Health is "" without a healthcheck, else "starting", "healthy" or "unhealthy".
	Health string
	// OpenStdin: the container was created with stdin open (needed for console commands).
	OpenStdin bool
}

func (c *Client) Inspect(ctx context.Context, container string) (State, error) {
	var body struct {
		State struct {
			Running   bool
			StartedAt string
			ExitCode  int
			Health    *struct{ Status string }
		}
		Config struct{ OpenStdin bool }
	}
	if err := c.call(ctx, "GET", "/containers/"+url.PathEscape(container)+"/json", &body); err != nil {
		return State{}, err
	}
	s := State{Running: body.State.Running, StartedAt: body.State.StartedAt, ExitCode: body.State.ExitCode, OpenStdin: body.Config.OpenStdin}
	if body.State.Health != nil {
		s.Health = body.State.Health.Status
	}
	return s, nil
}

// Start starts the container; starting a running container is not an error.
func (c *Client) Start(ctx context.Context, container string) error {
	return c.call(ctx, "POST", "/containers/"+url.PathEscape(container)+"/start", nil)
}

// Stop asks Docker to stop the container: SIGTERM (or its stop signal), then SIGKILL after timeout.
// It returns once the container is stopped. Stopping a stopped container is not an error.
func (c *Client) Stop(ctx context.Context, container string, timeout time.Duration) error {
	t := int(timeout.Round(time.Second) / time.Second)
	return c.call(ctx, "POST", "/containers/"+url.PathEscape(container)+"/stop?t="+strconv.Itoa(t), nil)
}

// response is the start of an HTTP response; the body is read from r.
type response struct {
	status int
	header map[string]string // lower-case names
	r      *bufio.Reader
}

// open sends a request without body and reads the status line and headers.
func (c *Client) open(ctx context.Context, method, path string, extraHeaders string) (net.Conn, *response, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, c.network, c.address)
	if err != nil {
		return nil, nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	request := method + " " + path + " HTTP/1.1\r\nHost: docker\r\nUser-Agent: sleepy\r\nContent-Length: 0\r\n" + extraHeaders + "\r\n"
	if _, err := io.WriteString(conn, request); err != nil {
		conn.Close()
		return nil, nil, err
	}
	resp, err := readHead(bufio.NewReader(conn))
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	return conn, resp, nil
}

func readHead(r *bufio.Reader) (*response, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return nil, err
	}
	parts := strings.SplitN(strings.TrimSpace(line), " ", 3)
	if len(parts) < 2 || !strings.HasPrefix(parts[0], "HTTP/") {
		return nil, fmt.Errorf("Docker API: invalid response %q", strings.TrimSpace(line))
	}
	status, err := strconv.Atoi(parts[1])
	if err != nil {
		return nil, fmt.Errorf("Docker API: invalid status %q", parts[1])
	}
	resp := &response{status: status, header: map[string]string{}, r: r}
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			return resp, nil
		}
		if name, value, ok := strings.Cut(line, ":"); ok {
			resp.header[strings.ToLower(strings.TrimSpace(name))] = strings.TrimSpace(value)
		}
	}
}

// body returns the response body, decoding chunked transfer encoding.
func (resp *response) body() io.Reader {
	if strings.EqualFold(resp.header["transfer-encoding"], "chunked") {
		return &chunkedReader{r: resp.r}
	}
	if n, err := strconv.ParseInt(resp.header["content-length"], 10, 64); err == nil {
		return io.LimitReader(resp.r, n)
	}
	return resp.r // Connection: close, the body ends with the connection
}

func (c *Client) call(ctx context.Context, method, path string, out any) error {
	conn, resp, err := c.open(ctx, method, path, "Connection: close\r\n")
	if err != nil {
		return err
	}
	defer conn.Close()
	body := io.LimitReader(resp.body(), 4<<20)
	switch {
	case resp.status == 304: // already started / stopped
		return nil
	case resp.status == 404:
		return ErrNotFound
	case resp.status >= 300:
		return apiError(resp.status, body)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(body).Decode(out)
}

func apiError(status int, body io.Reader) error {
	var msg struct{ Message string }
	raw, _ := io.ReadAll(io.LimitReader(body, 4096))
	if json.Unmarshal(raw, &msg) == nil && msg.Message != "" {
		return fmt.Errorf("Docker API: %s (HTTP %d)", msg.Message, status)
	}
	return fmt.Errorf("Docker API: HTTP %d %s", status, strings.TrimSpace(string(raw)))
}

// WriteStdin sends text to the container's stdin (its console), like `docker attach`.
// The container must have been created with stdin open (stdin_open: true).
func (c *Client) WriteStdin(ctx context.Context, container, text string) error {
	// The attach endpoint hijacks the connection: raw bytes flow both ways after the headers.
	path := "/containers/" + url.PathEscape(container) + "/attach?stream=1&stdin=1"
	conn, resp, err := c.open(ctx, "POST", path, "Content-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n")
	if err != nil {
		return err
	}
	defer conn.Close()
	switch {
	case resp.status == 404:
		return ErrNotFound
	case resp.status != 101 && resp.status != 200:
		return apiError(resp.status, resp.body())
	}
	if _, err := io.WriteString(conn, text); err != nil {
		return err
	}
	// Half-close, then wait briefly so Docker has copied the bytes before the stream ends.
	if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
	conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	io.Copy(io.Discard, resp.r)
	return nil
}

// chunkedReader decodes an HTTP/1.1 chunked body.
type chunkedReader struct {
	r    *bufio.Reader
	left int64
	done bool
}

func (c *chunkedReader) Read(p []byte) (int, error) {
	if c.done {
		return 0, io.EOF
	}
	if c.left == 0 {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return 0, err
		}
		size, _, _ := strings.Cut(strings.TrimSpace(line), ";")
		n, err := strconv.ParseInt(size, 16, 64)
		if err != nil {
			return 0, fmt.Errorf("Docker API: invalid chunk size %q", size)
		}
		if n == 0 {
			c.done = true
			return 0, io.EOF
		}
		c.left = n
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left == 0 && err == nil {
		// Each chunk ends with CRLF.
		if _, err := c.r.Discard(2); err != nil {
			return n, err
		}
	}
	return n, err
}
