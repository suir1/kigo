package netreuse

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestTCPDialerReusesListenerPort(t *testing.T) {
	if !Supported {
		t.Skip("same-port socket reuse is unsupported on this platform")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	reusableListener, err := ListenTCP(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer reusableListener.Close()
	localPort := tcpPort(t, reusableListener.Addr())

	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	targetAccepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := target.Accept()
		targetAccepted <- conn
	}()

	dialer := TCPDialer(localPort)
	outbound, err := dialer.DialContext(ctx, "tcp", target.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer outbound.Close()
	targetPeer := <-targetAccepted
	if targetPeer == nil {
		t.Fatal("target did not accept same-port outbound connection")
	}
	defer targetPeer.Close()
	if got := tcpPort(t, outbound.LocalAddr()); got != localPort {
		t.Fatalf("outbound local port = %d, want %d", got, localPort)
	}
	if got := tcpPort(t, targetPeer.RemoteAddr()); got != localPort {
		t.Fatalf("target observed port = %d, want %d", got, localPort)
	}

	inboundAccepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := reusableListener.Accept()
		inboundAccepted <- conn
	}()
	inbound, err := (&net.Dialer{}).DialContext(ctx, "tcp", reusableListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer inbound.Close()
	reusablePeer := <-inboundAccepted
	if reusablePeer == nil {
		t.Fatal("reusable listener stopped accepting after same-port outbound connection")
	}
	defer reusablePeer.Close()
}

func tcpPort(t *testing.T, address net.Addr) int {
	t.Helper()
	if address == nil {
		t.Fatal("missing TCP address")
	}
	_, portText, err := net.SplitHostPort(address.String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return port
}
