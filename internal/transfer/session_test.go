package transfer

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suir1/kigo/internal/mux"
	"github.com/suir1/kigo/internal/protocol"
	"github.com/suir1/kigo/internal/transport"
)

type countingTransport struct {
	transport.Transport
	sends atomic.Int32
}

func (t *countingTransport) Send(ctx context.Context, payload []byte) error {
	t.sends.Add(1)
	return t.Transport.Send(ctx, payload)
}

func TestTransferSessionProducesTypedEvents(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	item := protocol.Item{
		Kind:      protocol.ItemText,
		Name:      "message.txt",
		Size:      5,
		ChunkSize: protocol.ChunkSize,
	}
	sendErr := make(chan error, 1)
	go func() {
		session, err := NewSenderTransferSession(ctx, transport.NewTCPTransport(a), "ABC123")
		if err != nil {
			sendErr <- err
			return
		}
		if err := session.SendManifest(ctx, []protocol.Item{item}); err != nil {
			sendErr <- err
			return
		}
		if err := session.OpenStream(ctx, 0); err != nil {
			sendErr <- err
			return
		}
		if err := session.SendChunk(ctx, 0, 0, []byte("hello")); err != nil {
			sendErr <- err
			return
		}
		if err := session.EndStream(ctx, 0); err != nil {
			sendErr <- err
			return
		}
		if err := session.SendDone(ctx); err != nil {
			sendErr <- err
			return
		}
		sendErr <- session.WaitComplete(ctx)
	}()

	session, err := NewReceiverTransferSession(ctx, transport.NewTCPTransport(b), "ABC123")
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := []EventKind{EventManifest, EventStreamOpen, EventChunk, EventStreamEnd, EventDone}
	for _, want := range wantKinds {
		event, err := session.ReceiveEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if event.Kind != want {
			t.Fatalf("event kind = %q, want %q", event.Kind, want)
		}
		if event.Kind == EventChunk {
			if event.StreamID != 1 || event.ItemID != 0 || event.Offset != 0 || !bytes.Equal(event.Data, []byte("hello")) {
				t.Fatalf("unexpected chunk event: %#v", event)
			}
		}
	}
	if err := session.SendComplete(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-sendErr; err != nil {
		t.Fatal(err)
	}
}

func TestTransferSessionNegotiatesGzip(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	senderResult := make(chan *TransferSession, 1)
	senderErr := make(chan error, 1)
	go func() {
		session, err := NewSenderTransferSession(ctx, transport.NewTCPTransport(a), "ABC123")
		if err != nil {
			senderErr <- err
			return
		}
		senderResult <- session
	}()
	receiver, err := NewReceiverTransferSession(ctx, transport.NewTCPTransport(b), "ABC123")
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-senderErr:
		t.Fatal(err)
	case sender := <-senderResult:
		if sender.Compression() != compressionGzip || receiver.Compression() != compressionGzip {
			t.Fatalf("compression sender=%q receiver=%q", sender.Compression(), receiver.Compression())
		}
		if !sender.DeferredFileSHA256() || !receiver.DeferredFileSHA256() {
			t.Fatalf("deferred sha256 sender=%v receiver=%v", sender.DeferredFileSHA256(), receiver.DeferredFileSHA256())
		}
	}
}

func TestTransferSessionUsesIndependentDataConnections(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	senderChannels := make([]transport.Transport, 3)
	receiverChannels := make([]transport.Transport, 3)
	counts := make([]*countingTransport, 3)
	for index := range 3 {
		left, right := net.Pipe()
		counts[index] = &countingTransport{Transport: transport.NewTCPTransport(left)}
		senderChannels[index] = counts[index]
		receiverChannels[index] = transport.NewTCPTransport(right)
	}
	senderBundle := transport.NewBundle(senderChannels...)
	receiverBundle := transport.NewBundle(receiverChannels...)
	defer senderBundle.Close()
	defer receiverBundle.Close()

	items := []protocol.Item{{
		Kind:      protocol.ItemFile,
		Name:      "striped.bin",
		Size:      4,
		ChunkSize: protocol.ChunkSize,
	}}
	sendErr := make(chan error, 1)
	go func() {
		session, err := NewSenderTransferSession(ctx, senderBundle, "ABC123")
		if err != nil {
			sendErr <- err
			return
		}
		if session.ConnectionCount() != 3 {
			sendErr <- fmt.Errorf("sender connections = %d", session.ConnectionCount())
			return
		}
		if !session.StripesChunks() {
			sendErr <- errors.New("sender did not negotiate chunk striping")
			return
		}
		if err := session.SendManifest(ctx, items); err != nil {
			sendErr <- err
			return
		}
		if err := session.OpenStream(ctx, 0); err != nil {
			sendErr <- err
			return
		}
		for offset, data := range []byte("abcd") {
			if err := session.SendChunk(ctx, 0, int64(offset), []byte{data}); err != nil {
				sendErr <- err
				return
			}
		}
		if err := session.EndStream(ctx, 0); err != nil {
			sendErr <- err
			return
		}
		if err := session.SendDone(ctx); err != nil {
			sendErr <- err
			return
		}
		sendErr <- session.WaitComplete(ctx)
	}()

	receiver, err := NewReceiverTransferSession(ctx, receiverBundle, "ABC123")
	if err != nil {
		t.Fatal(err)
	}
	if receiver.ConnectionCount() != 3 {
		t.Fatalf("receiver connections = %d", receiver.ConnectionCount())
	}
	if !receiver.StripesChunks() {
		t.Fatal("receiver did not negotiate chunk striping")
	}
	chunks := make([]byte, 4)
	for {
		event, err := receiver.ReceiveEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if event.Kind == EventChunk {
			copy(chunks[event.Offset:], event.Data)
		}
		if event.Kind == EventDone {
			break
		}
	}
	if string(chunks) != "abcd" {
		t.Fatalf("chunks = %q", chunks)
	}
	if err := receiver.SendComplete(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-sendErr; err != nil {
		t.Fatal(err)
	}
	if counts[1].sends.Load() < 4 || counts[2].sends.Load() < 2 {
		t.Fatalf("data sends connection1=%d connection2=%d", counts[1].sends.Load(), counts[2].sends.Load())
	}
}

func TestSenderSessionDoesNotStripeWithoutPeerFeature(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	senderChannels := make([]transport.Transport, 2)
	receiverChannels := make([]transport.Transport, 2)
	for index := range 2 {
		left, right := net.Pipe()
		senderChannels[index] = transport.NewTCPTransport(left)
		receiverChannels[index] = transport.NewTCPTransport(right)
	}
	senderBundle := transport.NewBundle(senderChannels...)
	receiverBundle := transport.NewBundle(receiverChannels...)
	defer senderBundle.Close()
	defer receiverBundle.Close()

	receiverErr := make(chan error, 1)
	go func() {
		var hello helloMessage
		if err := recvPlain(ctx, receiverChannels[0], &hello); err != nil {
			receiverErr <- err
			return
		}
		receiverErr <- sendPlain(ctx, receiverChannels[0], helloMessage{
			Type:          "hello_ack",
			Version:       protocol.Version,
			ReceiverNonce: "legacy-mux-receiver",
			Connections:   2,
		})
	}()
	sender, err := NewSenderTransferSession(ctx, senderBundle, "ABC123")
	if err != nil {
		t.Fatal(err)
	}
	if err := <-receiverErr; err != nil {
		t.Fatal(err)
	}
	if sender.ConnectionCount() != 2 {
		t.Fatalf("connections = %d, want 2", sender.ConnectionCount())
	}
	if sender.StripesChunks() {
		t.Fatal("striping negotiated without peer feature")
	}
}

func TestWaitResumeAcceptDefersEarlyDataConnectionMessages(t *testing.T) {
	items := []protocol.Item{{
		Kind:            protocol.ItemFile,
		Name:            "early.bin",
		Size:            4,
		ChunkSize:       protocol.ChunkSize,
		ResumeSupported: true,
	}}
	manifest := protocol.NewManifest(items)
	plan := mux.NewPlan(len(items))
	plan.Apply(&manifest)
	session := newTransferSessionWithPipes([]*securePipe{
		{index: 0, striping: true},
		{index: 1, striping: true},
	}, false)
	session.receiveManifest = &manifest
	session.receivePlan = plan
	session.receiveStreams = mux.NewTracker(plan)
	session.receiveCoverage = map[int]*byteRanges{0: newByteRanges(0)}
	session.receiveMessages = make(chan receivedPipeMessage, 2)
	stream := 1
	session.receiveMessages <- receivedPipeMessage{
		pipeIndex: 1,
		message: protocol.Message{
			Type:   "stream_open",
			Item:   0,
			Stream: &stream,
		},
	}
	session.receiveMessages <- receivedPipeMessage{
		pipeIndex: 0,
		message: protocol.Message{
			Type: "resume_accept",
			Resume: []protocol.ResumeEntry{{
				Item:   0,
				Stream: &stream,
				Offset: 0,
			}},
		},
	}

	accepted, err := session.WaitResumeAccept(context.Background(), items)
	if err != nil {
		t.Fatal(err)
	}
	if len(accepted) != 1 || accepted[0].Item != 0 {
		t.Fatalf("accepted resume = %#v", accepted)
	}
	event, err := session.ReceiveEvent(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if event.Kind != EventStreamOpen || event.ItemID != 0 {
		t.Fatalf("deferred event = %#v", event)
	}
}

func TestStripedSessionDefersDoneUntilLateChunksArrive(t *testing.T) {
	pipes := []*securePipe{
		{index: 0, striping: true},
		{index: 1, striping: true},
		{index: 2, striping: true},
	}
	session := newTransferSessionWithPipes(pipes, false)
	session.receiveMessages = make(chan receivedPipeMessage, 6)
	manifest := protocol.NewManifest([]protocol.Item{{
		Kind:      protocol.ItemFile,
		Name:      "late.bin",
		Size:      4,
		ChunkSize: protocol.ChunkSize,
	}})
	mux.NewPlan(1).Apply(&manifest)
	stream := 1
	for _, received := range []receivedPipeMessage{
		{pipeIndex: 0, message: protocol.Message{Type: "manifest", Manifest: &manifest}},
		{pipeIndex: 1, message: protocol.Message{Type: "stream_open", Item: 0, Stream: &stream}},
		{pipeIndex: 1, message: protocol.Message{Type: "stream_end", Item: 0, Stream: &stream}},
		{pipeIndex: 0, message: protocol.Message{Type: "done"}},
		{pipeIndex: 2, message: protocol.Message{Type: "chunk", Item: 0, Stream: &stream, Offset: 2, Data: base64.StdEncoding.EncodeToString([]byte("te"))}},
		{pipeIndex: 1, message: protocol.Message{Type: "chunk", Item: 0, Stream: &stream, Offset: 0, Data: base64.StdEncoding.EncodeToString([]byte("la"))}},
	} {
		session.receiveMessages <- received
	}
	ctx := context.Background()
	for _, want := range []EventKind{EventManifest, EventStreamOpen, EventStreamEnd, EventChunk, EventChunk, EventDone} {
		event, err := session.ReceiveEvent(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if event.Kind != want {
			t.Fatalf("event kind = %q, want %q", event.Kind, want)
		}
	}
}

func TestSenderSessionFallsBackForLegacyReceiver(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	receiverErr := make(chan error, 1)
	go func() {
		tp := transport.NewTCPTransport(b)
		var hello helloMessage
		if err := recvPlain(ctx, tp, &hello); err != nil {
			receiverErr <- err
			return
		}
		receiverErr <- sendPlain(ctx, tp, helloMessage{
			Type:          "hello_ack",
			Version:       protocol.Version,
			ReceiverNonce: "legacy-receiver",
		})
	}()
	sender, err := NewSenderTransferSession(ctx, transport.NewTCPTransport(a), "ABC123")
	if err != nil {
		t.Fatal(err)
	}
	if err := <-receiverErr; err != nil {
		t.Fatal(err)
	}
	if sender.Compression() != "" {
		t.Fatalf("legacy receiver negotiated compression %q", sender.Compression())
	}
}
