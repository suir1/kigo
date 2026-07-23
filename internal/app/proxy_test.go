package app

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestInspectRelayThroughHTTPProxy(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	go func() {
		conn, err := target.Accept()
		if err == nil {
			defer conn.Close()
			_, _ = io.Copy(io.Discard, conn)
		}
	}()
	proxy := startAppHTTPConnectProxy(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	report := inspectRelay(ctx, target.Addr().String(), "http://"+proxy)
	if !report.OK || !report.ViaProxy || report.Error != "" {
		t.Fatalf("report = %#v", report)
	}
}

func TestInspectRelayRejectsInvalidProxy(t *testing.T) {
	report := inspectRelay(context.Background(), "relay.example:9000", "ftp://proxy.example:21")
	if report.OK || report.Error == "" {
		t.Fatalf("report = %#v", report)
	}
}

func startAppHTTPConnectProxy(t *testing.T) string {
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
			go func(conn net.Conn) {
				defer conn.Close()
				request, err := http.ReadRequest(bufio.NewReader(conn))
				if err != nil {
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
			}(conn)
		}
	}()
	return listener.Addr().String()
}
