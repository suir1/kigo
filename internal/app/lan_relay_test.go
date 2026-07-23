package app

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/suir1/kigo/internal/relay"
	"github.com/suir1/kigo/internal/transport"
)

func TestLANUpgradeSwitchesRelayPipeToDirectBundle(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	relaySender := transport.NewTCPTransport(left)
	relayReceiver := transport.NewTCPTransport(right)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const roomToken = "lan-upgrade-test-room"
	type result struct {
		transport transport.Transport
		count     int
		err       error
	}
	senderResult := make(chan result, 1)
	receiverResult := make(chan result, 1)
	go func() {
		tr, count, err := tryLANUpgradeSender(ctx, &globalOptions{
			DirectListen:    "127.0.0.1:0",
			DirectAdvertise: "127.0.0.1:1",
			Connections:     2,
		}, roomToken, relaySender)
		senderResult <- result{transport: tr, count: count, err: err}
	}()
	go func() {
		tr, count, err := tryLANUpgradeReceiver(ctx, &globalOptions{
			DirectListen: "127.0.0.1:0",
			Connections:  2,
		}, roomToken, relayReceiver)
		receiverResult <- result{transport: tr, count: count, err: err}
	}()
	sender := <-senderResult
	receiver := <-receiverResult
	if sender.err != nil || receiver.err != nil {
		t.Fatalf("LAN upgrade sender=%v receiver=%v", sender.err, receiver.err)
	}
	defer sender.transport.Close()
	defer receiver.transport.Close()
	if sender.count != 2 || receiver.count != 2 ||
		len(transport.Channels(sender.transport)) != 2 || len(transport.Channels(receiver.transport)) != 2 {
		t.Fatalf("LAN upgrade counts sender=%d receiver=%d", sender.count, receiver.count)
	}
	payload := []byte("direct after relay rendezvous")
	sendErr := make(chan error, 1)
	go func() { sendErr <- sender.transport.Send(ctx, payload) }()
	received, err := receiver.transport.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-sendErr; err != nil {
		t.Fatal(err)
	}
	if string(received) != string(payload) {
		t.Fatalf("received %q", received)
	}
}

func TestFailedLANUpgradeLeavesRelayPipeUsable(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	relaySender := transport.NewTCPTransport(left)
	relayReceiver := transport.NewTCPTransport(right)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	senderErr := make(chan error, 1)
	receiverErr := make(chan error, 1)
	go func() {
		_, _, err := tryLANUpgradeSender(ctx, &globalOptions{
			DirectListen: "bad-address",
			Connections:  1,
		}, "failed-lan-upgrade-room", relaySender)
		senderErr <- err
	}()
	go func() {
		_, _, err := tryLANUpgradeReceiver(ctx, &globalOptions{
			Connections: 1,
		}, "failed-lan-upgrade-room", relayReceiver)
		receiverErr <- err
	}()
	if err := <-senderErr; err == nil {
		t.Fatal("sender LAN upgrade unexpectedly succeeded")
	}
	if err := <-receiverErr; err == nil {
		t.Fatal("receiver LAN upgrade unexpectedly succeeded")
	}
	payload := []byte("relay remains available")
	sendErr := make(chan error, 1)
	go func() { sendErr <- relaySender.Send(ctx, payload) }()
	received, err := relayReceiver.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-sendErr; err != nil {
		t.Fatal(err)
	}
	if string(received) != string(payload) {
		t.Fatalf("received %q", received)
	}
}

func TestEmbeddedLANRelaySupportsLocalPairWithoutStandaloneRelay(t *testing.T) {
	discoveryAddr := freeUDPDiscoveryAddress(t)
	base := globalOptions{
		Local:          true,
		NoDirect:       true,
		NoTCPProbe:     true,
		Connections:    1,
		DiscoveryAddr:  discoveryAddr,
		LANTimeout:     500 * time.Millisecond,
		DirectListen:   "127.0.0.1:0",
		DirectTimeout:  100 * time.Millisecond,
		NoRouteHistory: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	const roomToken = "embedded-lan-relay-room"
	type result struct {
		transport transport.Transport
		err       error
	}
	senderResult := make(chan result, 1)
	receiverResult := make(chan result, 1)
	go func() {
		tr, err := dialRelayTransport(ctx, cloneGlobalOptions(&base), roomToken, "sender")
		senderResult <- result{transport: tr, err: err}
	}()
	time.Sleep(25 * time.Millisecond)
	go func() {
		tr, err := dialRelayTransport(ctx, cloneGlobalOptions(&base), roomToken, "receiver")
		receiverResult <- result{transport: tr, err: err}
	}()
	sender := <-senderResult
	receiver := <-receiverResult
	if sender.err != nil || receiver.err != nil {
		t.Fatalf("embedded relay sender=%v receiver=%v", sender.err, receiver.err)
	}
	defer sender.transport.Close()
	defer receiver.transport.Close()
	payload := []byte("embedded relay payload")
	sendErr := make(chan error, 1)
	go func() { sendErr <- sender.transport.Send(ctx, payload) }()
	received, err := receiver.transport.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-sendErr; err != nil {
		t.Fatal(err)
	}
	if string(received) != string(payload) {
		t.Fatalf("received %q", received)
	}
	for _, tr := range []transport.Transport{sender.transport, receiver.transport} {
		stats, ok := transport.SnapshotRouteStats(tr)
		if !ok || stats.Kind != historyRouteRelay {
			t.Fatalf("embedded relay stats = %#v ok=%v", stats, ok)
		}
	}
}

func TestLANUpgradeRequiresMutualCapabilityAndExternalRelay(t *testing.T) {
	g := &globalOptions{}
	join := relay.JoinOptions{Capabilities: []string{relay.CapabilityLANUpgradeV1}}
	result := relay.RaceResult{
		JoinResult: relay.JoinResult{PeerCapabilities: []string{relay.CapabilityLANUpgradeV1}},
		Candidate:  relay.Candidate{Kind: "external"},
	}
	if !shouldAttemptLANUpgrade(g, result, join) {
		t.Fatal("mutual LAN upgrade capability was ignored")
	}
	result.Candidate.Kind = "lan"
	if shouldAttemptLANUpgrade(g, result, join) {
		t.Fatal("LAN relay attempted a redundant LAN upgrade")
	}
	result.Candidate.Kind = "external"
	result.PeerCapabilities = nil
	if shouldAttemptLANUpgrade(g, result, join) {
		t.Fatal("one-sided LAN upgrade capability was accepted")
	}
	g.NoLAN = true
	result.PeerCapabilities = []string{relay.CapabilityLANUpgradeV1}
	if shouldAttemptLANUpgrade(g, result, join) {
		t.Fatal("--no-lan did not disable LAN upgrade")
	}
}

func TestRelayOnlyFallbackRetainsLANUpgradeAfterDirectAttempt(t *testing.T) {
	fallback := relayOnlyOptions(&globalOptions{})
	if !fallback.NoDirect || !fallback.allowLANUpgrade || !lanUpgradeEnabled(fallback) {
		t.Fatalf("relay fallback options = %#v", fallback)
	}
	userDisabled := relayOnlyOptions(&globalOptions{NoDirect: true})
	if lanUpgradeEnabled(userDisabled) {
		t.Fatal("user --no-direct unexpectedly retained LAN upgrade")
	}
	proxied := relayOnlyOptions(&globalOptions{Proxy: "socks5://127.0.0.1:1080"})
	if lanUpgradeEnabled(proxied) {
		t.Fatal("proxied relay fallback enabled direct LAN upgrade")
	}
}

func freeUDPDiscoveryAddress(t *testing.T) string {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	addr := conn.LocalAddr().String()
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}
