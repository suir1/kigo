package netproxy

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	tests := []struct {
		raw      string
		kind     proxyKind
		address  string
		username string
		password string
	}{
		{raw: "http://proxy.example", kind: proxyHTTP, address: "proxy.example:8080"},
		{raw: "http://user:p%40ss@proxy.example:3128", kind: proxyHTTP, address: "proxy.example:3128", username: "user", password: "p@ss"},
		{raw: "SOCKS5://user:pass@[::1]", kind: proxySOCKS5, address: "[::1]:1080", username: "user", password: "pass"},
	}
	for _, test := range tests {
		t.Run(test.raw, func(t *testing.T) {
			config, err := Parse(test.raw)
			if err != nil {
				t.Fatal(err)
			}
			if config.kind != test.kind || config.address != test.address ||
				config.username != test.username || config.password != test.password {
				t.Fatalf("config = %#v", config)
			}
		})
	}
	config, err := Parse("   ")
	if err != nil || config != nil {
		t.Fatalf("empty config = %#v, %v", config, err)
	}
}

func TestParseRejectsInvalidURLs(t *testing.T) {
	for _, raw := range []string{
		"proxy.example:8080",
		"https://proxy.example",
		"ftp://proxy.example",
		"http://",
		"http://proxy.example/path",
		"http://proxy.example?query=1",
		"http://proxy.example#fragment",
		"http://proxy.example:0",
		"http://proxy.example:",
		"http://proxy.example:70000",
		"http://:password@proxy.example",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := Parse(raw); err == nil {
				t.Fatal("invalid proxy URL was accepted")
			}
		})
	}
}

func TestHTTPConnectWithCredentials(t *testing.T) {
	target := startEchoServer(t)
	wantAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:secret"))
	proxyAddr := startHTTPProxy(t, http.StatusOK, wantAuth)
	config, err := Parse("http://alice:secret@" + proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	assertTunnel(t, config, target)
}

func TestProxyUsesConfiguredForwardDialer(t *testing.T) {
	target := startEchoServer(t)
	for _, scheme := range []string{"http", "socks5"} {
		t.Run(scheme, func(t *testing.T) {
			var proxyAddr string
			var raw string
			if scheme == "http" {
				proxyAddr = startHTTPProxy(t, http.StatusOK, "")
				raw = "http://" + proxyAddr
			} else {
				proxyAddr = startSOCKS5Proxy(t, "alice", "secret")
				raw = "socks5://alice:secret@" + proxyAddr
			}
			config, err := Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			called := make(chan string, 1)
			config = config.WithDialContext(func(ctx context.Context, network, address string) (net.Conn, error) {
				called <- address
				var dialer net.Dialer
				return dialer.DialContext(ctx, network, address)
			})
			assertTunnel(t, config, target)
			select {
			case address := <-called:
				if address != proxyAddr {
					t.Fatalf("forward dial address = %q, want %q", address, proxyAddr)
				}
			default:
				t.Fatal("configured forward dialer was not used")
			}
		})
	}
}

func TestHTTPConnectRejectsNonSuccess(t *testing.T) {
	proxyAddr := startHTTPProxy(t, http.StatusForbidden, "")
	config, err := Parse("http://" + proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := config.DialContext(ctx, "tcp", "127.0.0.1:9"); err == nil {
		t.Fatal("rejected CONNECT succeeded")
	}
}

func TestSOCKS5TunnelWithCredentials(t *testing.T) {
	target := startEchoServer(t)
	proxyAddr := startSOCKS5Proxy(t, "alice", "secret")
	config, err := Parse("socks5://alice:secret@" + proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	assertTunnel(t, config, target)
}

func TestDialContextCancelsStalledHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	config, err := Parse("http://" + listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err = config.DialContext(ctx, "tcp", "127.0.0.1:9")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	select {
	case conn := <-accepted:
		_ = conn.Close()
	case <-time.After(time.Second):
		t.Fatal("proxy did not accept connection")
	}
}

func assertTunnel(t *testing.T, config *Config, target string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := config.DialContext(ctx, "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("kigo-proxy")); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, len("kigo-proxy"))
	if _, err := io.ReadFull(conn, buffer); err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "kigo-proxy" {
		t.Fatalf("echo = %q", buffer)
	}
}

func startEchoServer(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener.Addr().String()
}

func startHTTPProxy(t *testing.T, status int, wantAuth string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveHTTPProxyConn(conn, status, wantAuth)
		}
	}()
	return listener.Addr().String()
}

func serveHTTPProxyConn(conn net.Conn, status int, wantAuth string) {
	defer conn.Close()
	request, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		return
	}
	if wantAuth != "" && request.Header.Get("Proxy-Authorization") != wantAuth {
		status = http.StatusProxyAuthRequired
	}
	if status != http.StatusOK {
		_, _ = fmt.Fprintf(conn, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\n\r\n", status, http.StatusText(status))
		return
	}
	target, err := net.Dial("tcp", request.Host)
	if err != nil {
		_, _ = io.WriteString(conn, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer target.Close()
	_, _ = io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
	go func() { _, _ = io.Copy(target, conn) }()
	_, _ = io.Copy(conn, target)
}

func startSOCKS5Proxy(t *testing.T, username, password string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveSOCKS5Conn(conn, username, password)
		}
	}()
	return listener.Addr().String()
}

func serveSOCKS5Conn(conn net.Conn, username, password string) {
	defer conn.Close()
	reader := bufio.NewReader(conn)
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil || header[0] != 5 {
		return
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return
	}
	if _, err := conn.Write([]byte{5, 2}); err != nil {
		return
	}
	if !readSOCKS5Auth(reader, username, password) {
		_, _ = conn.Write([]byte{1, 1})
		return
	}
	if _, err := conn.Write([]byte{1, 0}); err != nil {
		return
	}
	targetAddress, ok := readSOCKS5Target(reader)
	if !ok {
		return
	}
	target, err := net.Dial("tcp", targetAddress)
	if err != nil {
		_, _ = conn.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer target.Close()
	if _, err := conn.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	go func() { _, _ = io.Copy(target, reader) }()
	_, _ = io.Copy(conn, target)
}

func readSOCKS5Auth(reader *bufio.Reader, username, password string) bool {
	version, err := reader.ReadByte()
	if err != nil || version != 1 {
		return false
	}
	userLength, err := reader.ReadByte()
	if err != nil {
		return false
	}
	user := make([]byte, int(userLength))
	if _, err := io.ReadFull(reader, user); err != nil {
		return false
	}
	passwordLength, err := reader.ReadByte()
	if err != nil {
		return false
	}
	pass := make([]byte, int(passwordLength))
	if _, err := io.ReadFull(reader, pass); err != nil {
		return false
	}
	return string(user) == username && string(pass) == password
}

func readSOCKS5Target(reader *bufio.Reader) (string, bool) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(reader, header); err != nil || header[0] != 5 || header[1] != 1 {
		return "", false
	}
	var host string
	switch header[3] {
	case 1:
		address := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(reader, address); err != nil {
			return "", false
		}
		host = net.IP(address).String()
	case 3:
		length, err := reader.ReadByte()
		if err != nil {
			return "", false
		}
		address := make([]byte, int(length))
		if _, err := io.ReadFull(reader, address); err != nil {
			return "", false
		}
		host = string(address)
	case 4:
		address := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(reader, address); err != nil {
			return "", false
		}
		host = net.IP(address).String()
	default:
		return "", false
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(reader, portBytes); err != nil {
		return "", false
	}
	port := int(portBytes[0])<<8 | int(portBytes[1])
	return net.JoinHostPort(host, strconv.Itoa(port)), true
}
