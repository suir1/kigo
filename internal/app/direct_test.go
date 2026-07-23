package app

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/suir1/kigo/internal/directcandidate"
	"github.com/suir1/kigo/internal/transport"
)

func TestDirectConnectionRacesCandidatesAndVerifiesPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closedAddr := closedTCPAddress(t)
	senderCh := make(chan directConnectResult, 1)
	go func() {
		conn, err := acceptDirect(ctx, ln, 2*time.Second, "room-token")
		senderCh <- directConnectResult{conn: conn, err: err}
	}()

	receiver, err := connectDirect(ctx, []string{closedAddr, ln.Addr().String()}, 2*time.Second, "room-token")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	sender := <-senderCh
	if sender.err != nil {
		t.Fatal(sender.err)
	}
	defer sender.conn.Close()

	if _, err := receiver.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 2)
	if _, err := io.ReadFull(sender.conn, payload); err != nil {
		t.Fatal(err)
	}
	if string(payload) != "ok" {
		t.Fatalf("payload = %q", payload)
	}
}

func TestDirectListenerRejectsWrongRoomThenAcceptsPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	senderCh := make(chan directConnectResult, 1)
	go func() {
		conn, err := acceptDirect(ctx, ln, 2*time.Second, "correct-room")
		senderCh <- directConnectResult{conn: conn, err: err}
	}()

	if conn, err := connectDirect(ctx, []string{ln.Addr().String()}, 500*time.Millisecond, "wrong-room"); err == nil {
		_ = conn.Close()
		t.Fatal("wrong room token established a direct connection")
	}
	receiver, err := connectDirect(ctx, []string{ln.Addr().String()}, time.Second, "correct-room")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	sender := <-senderCh
	if sender.err != nil {
		t.Fatal(sender.err)
	}
	_ = sender.conn.Close()
}

func TestDirectBundleEstablishesIndexedConnections(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	type bundleResult struct {
		transport transport.Transport
		count     int
		err       error
	}
	senderCh := make(chan bundleResult, 1)
	go func() {
		tp, count, err := acceptDirectBundle(ctx, ln, 2*time.Second, "bundle-room", 4)
		senderCh <- bundleResult{transport: tp, count: count, err: err}
	}()
	receiver, receiverCount, err := connectDirectBundle(
		ctx,
		directConnectOptions{
			candidates:  []string{closedTCPAddress(t), ln.Addr().String()},
			timeout:     2 * time.Second,
			roomToken:   "bundle-room",
			connections: 4,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	sender := <-senderCh
	if sender.err != nil {
		t.Fatal(sender.err)
	}
	defer sender.transport.Close()
	if sender.count != 4 || receiverCount != 4 {
		t.Fatalf("sender count=%d receiver count=%d", sender.count, receiverCount)
	}
	senderChannels := transport.Channels(sender.transport)
	receiverChannels := transport.Channels(receiver)
	if len(senderChannels) != 4 || len(receiverChannels) != 4 {
		t.Fatalf("sender channels=%d receiver channels=%d", len(senderChannels), len(receiverChannels))
	}
	for index := range 4 {
		payload := []byte{byte('0' + index)}
		if err := receiverChannels[index].Send(ctx, payload); err != nil {
			t.Fatal(err)
		}
		got, err := senderChannels[index].Recv(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(payload) {
			t.Fatalf("connection %d got %q", index, got)
		}
	}
}

func TestDirectBundleUsesSmallerPeerConnectionCount(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	type bundleResult struct {
		transport transport.Transport
		count     int
		err       error
	}
	senderCh := make(chan bundleResult, 1)
	go func() {
		tp, count, err := acceptDirectBundle(ctx, ln, 2*time.Second, "count-room", 4)
		senderCh <- bundleResult{transport: tp, count: count, err: err}
	}()
	receiver, receiverCount, err := connectDirectBundle(ctx, directConnectOptions{
		candidates: []string{ln.Addr().String()}, timeout: 2 * time.Second, roomToken: "count-room", connections: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	sender := <-senderCh
	if sender.err != nil {
		t.Fatal(sender.err)
	}
	defer sender.transport.Close()
	if sender.count != 2 || receiverCount != 2 {
		t.Fatalf("sender count=%d receiver count=%d", sender.count, receiverCount)
	}
}

func TestDirectBundleFailsWhenAuxiliaryConnectionsDoNotArrive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	senderErr := make(chan error, 1)
	go func() {
		_, _, err := acceptDirectBundle(ctx, ln, 150*time.Millisecond, "partial-room", 3)
		senderErr <- err
	}()
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	verifyCtx, verifyCancel := context.WithTimeout(ctx, time.Second)
	defer verifyCancel()
	if _, err := verifyDirectReceiverIndexed(conn, verifyCtx, "partial-room", 0, 3); err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if err := <-senderErr; err == nil {
		t.Fatal("partial direct bundle was accepted")
	}
}

func TestAdvertisedDirectCandidatesAcceptsExplicitList(t *testing.T) {
	g := &globalOptions{
		DirectAdvertise: "10.0.0.2:4444,[fd00::2]:4444,10.0.0.2:4444,invalid",
	}
	got := advertisedDirectCandidates(g, mustResolveTCPAddr(t, "0.0.0.0:1234"))
	if len(got) != 2 {
		t.Fatalf("candidates = %#v", got)
	}
	if got[0] != "10.0.0.2:4444" || got[1] != "[fd00::2]:4444" {
		t.Fatalf("candidates = %#v", got)
	}
}

func TestAdvertisedDirectCandidateSetMarksExplicitAddressesManual(t *testing.T) {
	g := &globalOptions{
		DirectAdvertise: "10.0.0.2:4444,[2001:db8::2]:4444",
	}
	addresses, metadata := advertisedDirectCandidateSet(g, mustResolveTCPAddr(t, "0.0.0.0:1234"))
	if len(addresses) != 2 || len(metadata) != 2 {
		t.Fatalf("addresses=%#v metadata=%#v", addresses, metadata)
	}
	for _, candidate := range metadata {
		if candidate.Kind != directcandidate.KindManual ||
			candidate.Priority != directcandidate.PriorityManual {
			t.Fatalf("candidate = %#v", candidate)
		}
	}
}

func TestRankDirectCandidatesPrefersGlobalIPv6BeforeLAN(t *testing.T) {
	ranked := rankDirectCandidates([]string{
		"192.168.1.2:4000",
		"127.0.0.1:4000",
		"[2001:db8::2]:4000",
	}, nil)
	if len(ranked) != 3 {
		t.Fatalf("ranked = %#v", ranked)
	}
	if ranked[0].Address != "[2001:db8::2]:4000" || ranked[0].startDelay != 0 {
		t.Fatalf("IPv6 candidate = %#v", ranked[0])
	}
	if ranked[1].Address != "192.168.1.2:4000" ||
		ranked[1].startDelay != directLANStartDelay {
		t.Fatalf("LAN candidate = %#v", ranked[1])
	}
	if ranked[2].Address != "127.0.0.1:4000" ||
		ranked[2].startDelay != directOtherStartDelay {
		t.Fatalf("loopback candidate = %#v", ranked[2])
	}
}

func TestDirectConnectionFallsBackToDelayedLowerPriorityCandidate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	senderCh := make(chan directConnectResult, 1)
	go func() {
		conn, err := acceptDirect(ctx, ln, 2*time.Second, "delayed-room")
		senderCh <- directConnectResult{conn: conn, err: err}
	}()

	closedAddr := closedTCPAddress(t)
	metadata := []directcandidate.Candidate{
		{Address: closedAddr, Kind: directcandidate.KindManual, Priority: directcandidate.PriorityManual},
		{Address: ln.Addr().String(), Kind: directcandidate.KindLoopback, Priority: directcandidate.PriorityLoopback},
	}
	started := time.Now()
	receiver, err := connectDirectPrimary(ctx, directConnectOptions{
		candidates:  []string{closedAddr, ln.Addr().String()},
		metadata:    metadata,
		timeout:     time.Second,
		roomToken:   "delayed-room",
		connections: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.conn.Close()
	if elapsed := time.Since(started); elapsed < directOtherStartDelay/2 {
		t.Fatalf("lower-priority candidate started too early: %s", elapsed)
	}
	sender := <-senderCh
	if sender.err != nil {
		t.Fatal(sender.err)
	}
	_ = sender.conn.Close()
}

func TestDirectHandshakeDoesNotConsumeFollowingTransportBytes(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	serverErr := make(chan error, 1)
	go func() {
		hello, err := readDirectHello(server)
		if err != nil {
			serverErr <- err
			return
		}
		if hello.RoomToken != "room-token" {
			serverErr <- io.ErrUnexpectedEOF
			return
		}
		if err := writeDirectHello(server, directHello{
			Protocol:  directProtocol,
			RoomToken: "room-token",
			Role:      "sender",
		}); err != nil {
			serverErr <- err
			return
		}
		_, err = server.Write([]byte("next-frame"))
		serverErr <- err
	}()

	if err := verifyDirectReceiver(client, ctx, "room-token"); err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, len("next-frame"))
	if _, err := io.ReadFull(client, payload); err != nil {
		t.Fatal(err)
	}
	if string(payload) != "next-frame" {
		t.Fatalf("payload = %q", payload)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func closedTCPAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}
