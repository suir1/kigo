package app

import (
	"bytes"
	"context"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/suir1/kigo/internal/relay"
	"github.com/suir1/kigo/internal/transfer"
	"github.com/suir1/kigo/internal/transport"
)

func TestBidirectionalDirectBundleEstablishesIndexedConnections(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	senderListener, err := listenDirect(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	receiverListener, err := listenDirect(ctx, "127.0.0.1:0")
	if err != nil {
		_ = senderListener.Close()
		t.Fatal(err)
	}
	type result struct {
		transport transport.Transport
		count     int
		err       error
	}
	punchAt := time.Now().Add(50 * time.Millisecond)
	senderCh := make(chan result, 1)
	go func() {
		tp, count, err := connectBidirectionalDirectBundle(
			ctx,
			senderListener,
			"sender",
			punchAt,
			directConnectOptions{
				candidates:  []string{receiverListener.Addr().String()},
				timeout:     2 * time.Second,
				roomToken:   "bidirectional-bundle-room",
				connections: 4,
			},
		)
		senderCh <- result{transport: tp, count: count, err: err}
	}()
	receiver, receiverCount, err := connectBidirectionalDirectBundle(
		ctx,
		receiverListener,
		"receiver",
		punchAt,
		directConnectOptions{
			candidates:  []string{senderListener.Addr().String()},
			timeout:     2 * time.Second,
			roomToken:   "bidirectional-bundle-room",
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

func TestBidirectionalDirectFallsBackToSenderInitiatedConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	senderListener, err := listenDirect(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer senderListener.Close()
	receiverListener, err := listenDirect(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer receiverListener.Close()
	closedSenderAddress := closedTCPAddress(t)
	type result struct {
		primary bidirectionalPrimaryResult
		err     error
	}
	senderCh := make(chan result, 1)
	go func() {
		primary, err := connectBidirectionalDirectPrimary(
			ctx,
			senderListener,
			"sender",
			time.Time{},
			directConnectOptions{
				candidates:  []string{receiverListener.Addr().String()},
				timeout:     400 * time.Millisecond,
				roomToken:   "bidirectional-fallback-room",
				connections: 1,
			},
		)
		senderCh <- result{primary: primary, err: err}
	}()
	receiver, err := connectBidirectionalDirectPrimary(
		ctx,
		receiverListener,
		"receiver",
		time.Time{},
		directConnectOptions{
			candidates:  []string{closedSenderAddress},
			timeout:     400 * time.Millisecond,
			roomToken:   "bidirectional-fallback-room",
			connections: 1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.conn.Close()
	sender := <-senderCh
	if sender.err != nil {
		t.Fatal(sender.err)
	}
	defer sender.primary.conn.Close()
	if sender.primary.initiatorRole != "sender" || receiver.initiatorRole != "sender" {
		t.Fatalf(
			"sender initiator=%q receiver initiator=%q",
			sender.primary.initiatorRole,
			receiver.initiatorRole,
		)
	}
}

func TestBidirectionalDirectPrimaryPrefersReceiverInitiatedConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	senderListener, err := listenDirect(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer senderListener.Close()
	receiverListener, err := listenDirect(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer receiverListener.Close()
	type result struct {
		primary bidirectionalPrimaryResult
		err     error
	}
	senderCh := make(chan result, 1)
	started := time.Now()
	go func() {
		primary, err := connectBidirectionalDirectPrimary(
			ctx,
			senderListener,
			"sender",
			time.Now().Add(50*time.Millisecond),
			directConnectOptions{
				candidates:  []string{receiverListener.Addr().String()},
				timeout:     2 * time.Second,
				roomToken:   "bidirectional-preferred-room",
				connections: 1,
			},
		)
		senderCh <- result{primary: primary, err: err}
	}()
	receiver, err := connectBidirectionalDirectPrimary(
		ctx,
		receiverListener,
		"receiver",
		time.Now().Add(50*time.Millisecond),
		directConnectOptions{
			candidates:  []string{senderListener.Addr().String()},
			timeout:     2 * time.Second,
			roomToken:   "bidirectional-preferred-room",
			connections: 1,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.conn.Close()
	sender := <-senderCh
	if sender.err != nil {
		t.Fatal(sender.err)
	}
	defer sender.primary.conn.Close()
	if sender.primary.initiatorRole != preferredDirectInitiator ||
		receiver.initiatorRole != preferredDirectInitiator {
		t.Fatalf(
			"sender initiator=%q receiver initiator=%q",
			sender.primary.initiatorRole,
			receiver.initiatorRole,
		)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("preferred direct selection took %s", elapsed)
	}
}

func TestRelayBidirectionalDirectTransfersStripedFile(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	relayListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := relay.NewServer().Serve(ctx, relayListener); err != nil {
			t.Errorf("relay serve failed: %v", err)
		}
	}()

	root := t.TempDir()
	sourcePath := filepath.Join(root, "source.bin")
	source := bytes.Repeat([]byte("kigo-striped-direct-"), 32*1024)
	if err := os.WriteFile(sourcePath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(root, "out")
	if err := os.Mkdir(outputDir, 0o700); err != nil {
		t.Fatal(err)
	}
	options := &globalOptions{
		Relay:             relayListener.Addr().String(),
		NoLAN:             true,
		DirectListen:      "127.0.0.1:0",
		DirectTimeout:     900 * time.Millisecond,
		Connections:       4,
		NoRouteHistory:    true,
		ReconnectDelay:    time.Millisecond,
		ReconnectAttempts: 1,
	}
	const roomToken = "relay-bidirectional-striped-room"
	type dialResult struct {
		transport transport.Transport
		err       error
	}
	senderDial := make(chan dialResult, 1)
	go func() {
		tp, err := dialRelayTransport(ctx, options, roomToken, "sender")
		senderDial <- dialResult{transport: tp, err: err}
	}()
	receiverTransport, err := dialRelayTransport(ctx, options, roomToken, "receiver")
	if err != nil {
		t.Fatal(err)
	}
	defer receiverTransport.Close()
	sender := <-senderDial
	if sender.err != nil {
		t.Fatal(sender.err)
	}
	defer sender.transport.Close()
	if len(transport.Channels(sender.transport)) != 4 ||
		len(transport.Channels(receiverTransport)) != 4 {
		t.Fatalf(
			"sender channels=%d receiver channels=%d",
			len(transport.Channels(sender.transport)),
			len(transport.Channels(receiverTransport)),
		)
	}

	prepared, err := transfer.PreparePathWithOptions(sourcePath, transfer.PrepareOptions{Symlinks: transfer.SymlinkFollow})
	if err != nil {
		t.Fatal(err)
	}
	sendErr := make(chan error, 1)
	go func() {
		sendErr <- prepared.Send(ctx, sender.transport, transfer.SenderOptions{
			Code: "ABC123",
		})
	}()
	if _, err := transfer.Receive(ctx, receiverTransport, transfer.ReceiverOptions{
		Code:      "ABC123",
		OutputDir: outputDir,
	}); err != nil {
		t.Fatal(err)
	}
	if err := <-sendErr; err != nil {
		t.Fatal(err)
	}
	received, err := os.ReadFile(filepath.Join(outputDir, "source.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received, source) {
		t.Fatalf("received %d bytes, want %d", len(received), len(source))
	}
}
