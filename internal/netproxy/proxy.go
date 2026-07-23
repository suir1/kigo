package netproxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	xproxy "golang.org/x/net/proxy"
)

const handshakeTimeout = 10 * time.Second

type proxyKind string

const (
	proxyHTTP   proxyKind = "http"
	proxySOCKS5 proxyKind = "socks5"
)

// Config is a validated outbound TCP proxy configuration.
type Config struct {
	kind     proxyKind
	address  string
	username string
	password string
	dial     DialContextFunc
}

type DialContextFunc func(context.Context, string, string) (net.Conn, error)

type contextDialer struct {
	dial DialContextFunc
}

func (d contextDialer) Dial(network, address string) (net.Conn, error) {
	return d.dial(context.Background(), network, address)
}

func (d contextDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.dial(ctx, network, address)
}

// Parse validates an HTTP CONNECT or SOCKS5 proxy URL. An empty value disables proxying.
func Parse(raw string) (*Config, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL: %w", err)
	}
	if u.Opaque != "" || u.Host == "" || u.Hostname() == "" {
		return nil, errors.New("proxy URL must include a host")
	}
	if strings.HasSuffix(u.Host, ":") {
		return nil, errors.New("proxy URL has an empty port")
	}
	if u.Path != "" || u.RawPath != "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("proxy URL must not include a path, query, or fragment")
	}

	var kind proxyKind
	var defaultPort string
	switch strings.ToLower(u.Scheme) {
	case "http":
		kind = proxyHTTP
		defaultPort = "8080"
	case "socks5":
		kind = proxySOCKS5
		defaultPort = "1080"
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q; use http or socks5", u.Scheme)
	}

	port := u.Port()
	if port == "" {
		port = defaultPort
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return nil, fmt.Errorf("invalid proxy port %q", port)
	}

	var username, password string
	if u.User != nil {
		username = u.User.Username()
		password, _ = u.User.Password()
		if username == "" {
			return nil, errors.New("proxy credentials require a username")
		}
		if kind == proxySOCKS5 && (len(username) > 255 || len(password) > 255) {
			return nil, errors.New("SOCKS5 username and password must be at most 255 bytes")
		}
	}

	return &Config{
		kind:     kind,
		address:  net.JoinHostPort(u.Hostname(), port),
		username: username,
		password: password,
	}, nil
}

// WithDialContext returns a copy that reaches the proxy through the supplied dialer.
func (c *Config) WithDialContext(dial DialContextFunc) *Config {
	if c == nil || dial == nil {
		return c
	}
	clone := *c
	clone.dial = dial
	return &clone
}

// DialContext opens a TCP connection to address through the configured proxy.
func (c *Config) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if c == nil {
		return nil, errors.New("proxy configuration is nil")
	}
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return nil, fmt.Errorf("proxy only supports TCP, got %q", network)
	}
	if err := validateTarget(address); err != nil {
		return nil, err
	}
	handshakeCtx, cancel := context.WithTimeout(ctx, handshakeTimeout)
	defer cancel()

	switch c.kind {
	case proxyHTTP:
		return c.dialHTTP(handshakeCtx, address)
	case proxySOCKS5:
		return c.dialSOCKS5(handshakeCtx, address)
	default:
		return nil, errors.New("unsupported proxy configuration")
	}
}

func (c *Config) dialHTTP(ctx context.Context, address string) (net.Conn, error) {
	conn, err := c.dialProxy(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect HTTP proxy: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = conn.Close()
		}
	}()

	stopWatch := make(chan struct{})
	defer close(stopWatch)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stopWatch:
		}
	}()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	request := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: address},
		Host:   address,
		Header: http.Header{"Proxy-Connection": []string{"keep-alive"}},
	}
	if c.username != "" {
		credentials := base64.StdEncoding.EncodeToString([]byte(c.username + ":" + c.password))
		request.Header.Set("Proxy-Authorization", "Basic "+credentials)
	}
	if err := request.Write(conn); err != nil {
		if contextErr := handshakeContextError(ctx, err); contextErr != nil {
			return nil, contextErr
		}
		return nil, fmt.Errorf("write HTTP CONNECT request: %w", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), request)
	if err != nil {
		if contextErr := handshakeContextError(ctx, err); contextErr != nil {
			return nil, contextErr
		}
		return nil, fmt.Errorf("read HTTP CONNECT response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		if response.Body != nil {
			_ = response.Body.Close()
		}
		return nil, fmt.Errorf("HTTP CONNECT rejected: %s", response.Status)
	}
	if err := conn.SetDeadline(time.Time{}); err != nil {
		return nil, fmt.Errorf("clear HTTP proxy deadline: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	keep = true
	return conn, nil
}

func (c *Config) dialSOCKS5(ctx context.Context, address string) (net.Conn, error) {
	var auth *xproxy.Auth
	if c.username != "" {
		auth = &xproxy.Auth{User: c.username, Password: c.password}
	}
	forward := xproxy.Dialer(&net.Dialer{})
	if c.dial != nil {
		forward = contextDialer{dial: c.dial}
	}
	dialer, err := xproxy.SOCKS5("tcp", c.address, auth, forward)
	if err != nil {
		return nil, fmt.Errorf("configure SOCKS5 proxy: %w", err)
	}
	contextDialer, ok := dialer.(xproxy.ContextDialer)
	if !ok {
		return nil, errors.New("SOCKS5 proxy does not support context cancellation")
	}
	conn, err := contextDialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, fmt.Errorf("SOCKS5 connect: %w", err)
	}
	return conn, nil
}

func (c *Config) dialProxy(ctx context.Context) (net.Conn, error) {
	if c.dial != nil {
		return c.dial(ctx, "tcp", c.address)
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", c.address)
}

func validateTarget(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || host == "" {
		return fmt.Errorf("proxy target must be host:port: %q", address)
	}
	if strings.IndexFunc(host, func(r rune) bool { return r < 0x20 || r == 0x7f }) >= 0 {
		return errors.New("proxy target host contains control characters")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("invalid proxy target port %q", port)
	}
	return nil
}

func handshakeContextError(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			return context.DeadlineExceeded
		}
	}
	return nil
}
