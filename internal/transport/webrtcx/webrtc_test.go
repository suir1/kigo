package webrtcx

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	"github.com/suir1/kigo/internal/transport"
)

func TestNewPeerConnectionAcceptsInterfaceAndIPFilters(t *testing.T) {
	pc, err := newPeerConnection(withDefaults(Options{
		InterfaceFilter: func(name string) bool { return name == "test-interface" },
		IPFilter:        func(ip net.IP) bool { return ip.IsLoopback() },
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestNewPeerConnectionConnectsOverSelectedLoopback(t *testing.T) {
	interfaceName := loopbackInterfaceName(t)
	options := Options{
		ICEServers:               []webrtc.ICEServer{},
		InterfaceFilter:          func(name string) bool { return name == interfaceName },
		IPFilter:                 func(ip net.IP) bool { return ip.IsLoopback() },
		IncludeLoopbackCandidate: true,
	}
	newPC := func() (*webrtc.PeerConnection, error) {
		return newPeerConnection(options)
	}
	left, right := newDataChannelTransportPairWithFactory(t, newPC)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := left.Send(ctx, []byte("loopback")); err != nil {
		t.Fatal(err)
	}
	if received, err := right.Recv(ctx); err != nil || string(received) != "loopback" {
		t.Fatalf("loopback receive = %q, %v", received, err)
	}
}

func TestSignalDialerClonesTLSConfig(t *testing.T) {
	config := &tls.Config{MinVersion: tls.VersionTLS13}
	dialer := signalDialer(Options{TLSClientConfig: config})
	if dialer.TLSClientConfig == nil || dialer.TLSClientConfig.MinVersion != tls.VersionTLS13 {
		t.Fatalf("TLS config = %#v", dialer.TLSClientConfig)
	}
	if dialer.TLSClientConfig == config {
		t.Fatal("signal dialer retained caller TLS config pointer")
	}
}

func TestReconnectState(t *testing.T) {
	var state ReconnectState
	if state.Supported() || state.Token() != "" || state.Generation() != 0 {
		t.Fatal("zero reconnect state is not empty")
	}
	state.update("secret-token", 7)
	if !state.Supported() {
		t.Fatal("updated reconnect state is not supported")
	}
	if state.Token() != "secret-token" {
		t.Fatalf("token = %q", state.Token())
	}
	if state.Generation() != 7 {
		t.Fatalf("generation = %d", state.Generation())
	}
	state.disable()
	if state.Supported() || state.Token() != "" || state.Generation() != 0 {
		t.Fatal("disabled reconnect state retained capability data")
	}
}

func TestSignalURLIncludesRoleButNotReconnectToken(t *testing.T) {
	got := signalURL("https://kigo.example/base", strings.Repeat("a", 64), "receiver")
	want := "wss://kigo.example/base/api/signal/" + strings.Repeat("a", 64) + "?role=receiver"
	if got != want {
		t.Fatalf("signal URL = %q, want %q", got, want)
	}
	if strings.Contains(got, "secret") || strings.Contains(got, "reconnect") {
		t.Fatalf("signal URL exposed reconnect data: %q", got)
	}
}

func TestSignalURLIncludesNoteProtocol(t *testing.T) {
	token := strings.Repeat("b", 64)
	got := signalURL("https://kigo.example/base", token, "sender", "note")
	want := "wss://kigo.example/base/api/signal/" + token + "?protocol=note&role=sender"
	if got != want {
		t.Fatalf("note signal URL = %q, want %q", got, want)
	}
}

func TestDataChannelTransportTransfersAndPropagatesClose(t *testing.T) {
	left, right := newDataChannelTransportPair(t)
	payload := bytes.Repeat([]byte("kigo"), 16*1024)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := left.Send(ctx, payload); err != nil {
		t.Fatal(err)
	}
	received, err := right.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received, payload) {
		t.Fatalf("received %d bytes, want %d", len(received), len(payload))
	}
	if metrics := left.SendMetrics(); metrics.BufferLimit != dataChannelBufferedHigh {
		t.Fatalf("send metrics = %#v", metrics)
	}

	if err := left.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := right.Recv(ctx); !errors.Is(err, transport.ErrClosed) {
		t.Fatalf("receive after peer close = %v, want %v", err, transport.ErrClosed)
	}
}

func TestDataChannelTransportBackpressureHonorsContextAndLowSignal(t *testing.T) {
	channel := &stubDataChannel{}
	channel.buffered.Store(dataChannelBufferedHigh + 1)
	transport := &DataChannelTransport{
		dc: channel, recv: make(chan []byte), opened: make(chan struct{}),
		closed: make(chan struct{}), low: make(chan struct{}, 1),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := transport.waitBuffered(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waitBuffered error = %v, want %v", err, context.DeadlineExceeded)
	}

	channel.buffered.Store(0)
	transport.low <- struct{}{}
	if err := transport.waitBuffered(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := transport.Send(context.Background(), []byte("ready")); err != nil {
		t.Fatal(err)
	}
	if channel.sends.Load() != 1 {
		t.Fatalf("data channel sends = %d, want 1", channel.sends.Load())
	}
}

type stubDataChannel struct {
	buffered atomic.Uint64
	sends    atomic.Uint64
}

func (c *stubDataChannel) Send([]byte) error {
	c.sends.Add(1)
	return nil
}

func (*stubDataChannel) Close() error {
	return nil
}

func (c *stubDataChannel) BufferedAmount() uint64 {
	return c.buffered.Load()
}

func newDataChannelTransportPair(t *testing.T) (*DataChannelTransport, *DataChannelTransport) {
	t.Helper()
	configuration := webrtc.Configuration{}
	return newDataChannelTransportPairWithFactory(t, func() (*webrtc.PeerConnection, error) {
		return webrtc.NewPeerConnection(configuration)
	})
}

func newDataChannelTransportPairWithFactory(
	t *testing.T,
	newPC func() (*webrtc.PeerConnection, error),
) (*DataChannelTransport, *DataChannelTransport) {
	t.Helper()
	leftPC, err := newPC()
	if err != nil {
		t.Fatal(err)
	}
	rightPC, err := newPC()
	if err != nil {
		_ = leftPC.Close()
		t.Fatal(err)
	}
	leftDC, err := leftPC.CreateDataChannel("kigo-test", nil)
	if err != nil {
		_ = leftPC.Close()
		_ = rightPC.Close()
		t.Fatal(err)
	}
	left := wrap(leftPC, leftDC, nil)
	rightReady := make(chan *DataChannelTransport, 1)
	rightPC.OnDataChannel(func(channel *webrtc.DataChannel) {
		rightReady <- wrap(rightPC, channel, nil)
	})

	offer, err := leftPC.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	leftGathered := webrtc.GatheringCompletePromise(leftPC)
	if err := leftPC.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	<-leftGathered
	if err := rightPC.SetRemoteDescription(*leftPC.LocalDescription()); err != nil {
		t.Fatal(err)
	}
	answer, err := rightPC.CreateAnswer(nil)
	if err != nil {
		t.Fatal(err)
	}
	rightGathered := webrtc.GatheringCompletePromise(rightPC)
	if err := rightPC.SetLocalDescription(answer); err != nil {
		t.Fatal(err)
	}
	<-rightGathered
	if err := leftPC.SetRemoteDescription(*rightPC.LocalDescription()); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var right *DataChannelTransport
	select {
	case right = <-rightReady:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := left.waitOpen(ctx); err != nil {
		t.Fatal(err)
	}
	if err := right.waitOpen(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = left.Close()
		_ = right.Close()
	})
	return left, right
}

func loopbackInterfaceName(t *testing.T) string {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp != 0 && iface.Flags&net.FlagLoopback != 0 {
			return iface.Name
		}
	}
	t.Fatal("no active loopback interface found")
	return ""
}
