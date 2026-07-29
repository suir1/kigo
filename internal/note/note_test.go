package note

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suir1/kigo/internal/transport"
)

func TestWorkspaceUsesMonotonicRevisionsAndDeterministicConflicts(t *testing.T) {
	workspace := NewWorkspace()
	first, err := workspace.Update(DefaultPad, "first", time.UnixMilli(100))
	if err != nil {
		t.Fatal(err)
	}
	second, err := workspace.Update(DefaultPad, "second", time.UnixMilli(100))
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || second.Revision != 2 || second.Timestamp != 101 {
		t.Fatalf("documents = %#v %#v", first, second)
	}
	applied, current, err := workspace.ApplyRemote(Document{
		Pad:       DefaultPad,
		Text:      "remote",
		Revision:  1,
		Timestamp: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if applied || current.Text != "second" {
		t.Fatalf("stale remote update applied=%v current=%#v", applied, current)
	}
	applied, current, err = workspace.ApplyRemote(Document{
		Pad:       DefaultPad,
		Text:      "remote",
		Revision:  3,
		Timestamp: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !applied || current.Text != "remote" {
		t.Fatalf("new remote update applied=%v current=%#v", applied, current)
	}
}

func TestFrameRejectsOversizedText(t *testing.T) {
	frame := Frame{
		Type:      FrameUpdate,
		Version:   ProtocolVersion,
		Pad:       DefaultPad,
		Revision:  1,
		Timestamp: 1,
		Text:      strings.Repeat("x", MaxTextSize+1),
	}
	if err := frame.Validate(); err == nil {
		t.Fatal("expected oversized text to be rejected")
	}
}

func TestSessionRejectsReplayAndTampering(t *testing.T) {
	hostTransport, joinTransport := newQueuedPair()
	defer hostTransport.Close()
	defer joinTransport.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	type sessionResult struct {
		session *Session
		err     error
	}
	hostResult := make(chan sessionResult, 1)
	go func() {
		session, err := NewHost(ctx, hostTransport, "ABC123")
		hostResult <- sessionResult{session: session, err: err}
	}()
	join, err := NewJoin(ctx, joinTransport, "ABC123")
	if err != nil {
		t.Fatal(err)
	}
	hostResultValue := <-hostResult
	if hostResultValue.err != nil {
		t.Fatal(hostResultValue.err)
	}
	host := hostResultValue.session
	defer host.Close()
	defer join.Close()

	if err := host.Send(ctx, Frame{
		Type:      FrameUpdate,
		Version:   ProtocolVersion,
		Pad:       DefaultPad,
		Revision:  1,
		Timestamp: 1,
		Text:      "one",
	}); err != nil {
		t.Fatal(err)
	}
	wire := <-joinTransport.incoming
	joinTransport.incoming <- append([]byte(nil), wire...)
	if _, err := join.Recv(ctx); err != nil {
		t.Fatal(err)
	}
	joinTransport.incoming <- append([]byte(nil), wire...)
	if _, err := join.Recv(ctx); err == nil || !strings.Contains(err.Error(), "sequence") {
		t.Fatalf("replay error = %v", err)
	}

	if err := host.Send(ctx, Frame{
		Type:      FrameUpdate,
		Version:   ProtocolVersion,
		Pad:       DefaultPad,
		Revision:  2,
		Timestamp: 2,
		Text:      "two",
	}); err != nil {
		t.Fatal(err)
	}
	wire = <-joinTransport.incoming
	var tamperedEnvelope envelope
	if err := json.Unmarshal(wire, &tamperedEnvelope); err != nil {
		t.Fatal(err)
	}
	tamperedEnvelope.Ciphertext[len(tamperedEnvelope.Ciphertext)-1] ^= 1
	tampered, err := json.Marshal(tamperedEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	joinTransport.incoming <- tampered
	if _, err := join.Recv(ctx); err == nil || !strings.Contains(err.Error(), "decrypt") {
		t.Fatalf("tamper error = %v", err)
	}
}

func TestHostAndJoinExchangeEncryptedFramesBidirectionally(t *testing.T) {
	left, right := net.Pipe()
	hostTransport := transport.NewTCPTransport(left)
	joinTransport := transport.NewTCPTransport(right)
	defer hostTransport.Close()
	defer joinTransport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	type sessionResult struct {
		session *Session
		err     error
	}
	hostResult := make(chan sessionResult, 1)
	go func() {
		session, err := NewHost(ctx, hostTransport, "ABC123")
		hostResult <- sessionResult{session: session, err: err}
	}()
	join, err := NewJoin(ctx, joinTransport, "ABC123")
	if err != nil {
		t.Fatal(err)
	}
	hostResultValue := <-hostResult
	if hostResultValue.err != nil {
		t.Fatal(hostResultValue.err)
	}
	host := hostResultValue.session
	defer host.Close()
	defer join.Close()

	hostSend := make(chan error, 1)
	go func() {
		hostSend <- host.Send(ctx, FrameFromDocument(FrameUpdate, Document{
			Pad:       DefaultPad,
			Text:      "from host",
			Revision:  1,
			Timestamp: 1,
		}))
	}()
	frame, err := join.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if frame.Text != "from host" {
		t.Fatalf("join received %#v", frame)
	}
	if err := <-hostSend; err != nil {
		t.Fatal(err)
	}

	joinAck := make(chan error, 1)
	go func() {
		joinAck <- join.Send(ctx, Frame{
			Type:     FrameAck,
			Version:  ProtocolVersion,
			Pad:      DefaultPad,
			Revision: 1,
		})
	}()
	ack, err := host.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ack.Type != FrameAck || ack.Revision != 1 {
		t.Fatalf("host received %#v", ack)
	}
	if err := <-joinAck; err != nil {
		t.Fatal(err)
	}
}

func TestSessionSyncWorkspaceConvergesAfterReconnect(t *testing.T) {
	left, right := net.Pipe()
	hostTransport := transport.NewTCPTransport(left)
	joinTransport := transport.NewTCPTransport(right)
	defer hostTransport.Close()
	defer joinTransport.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	type sessionResult struct {
		session *Session
		err     error
	}
	hostResult := make(chan sessionResult, 1)
	go func() {
		session, err := NewHost(ctx, hostTransport, "ABC123")
		hostResult <- sessionResult{session: session, err: err}
	}()
	join, err := NewJoin(ctx, joinTransport, "ABC123")
	if err != nil {
		t.Fatal(err)
	}
	hostValue := <-hostResult
	if hostValue.err != nil {
		t.Fatal(hostValue.err)
	}
	host := hostValue.session
	defer host.Close()
	defer join.Close()
	if !host.WorkspaceSyncSupported() || !join.WorkspaceSyncSupported() {
		t.Fatal("workspace sync capability was not negotiated")
	}

	hostWorkspace := NewWorkspace()
	joinWorkspace := NewWorkspace()
	if _, err := hostWorkspace.Update("Sprint Notes", "host draft", time.UnixMilli(100)); err != nil {
		t.Fatal(err)
	}
	if _, err := joinWorkspace.Update("Sprint Notes", "join draft", time.UnixMilli(200)); err != nil {
		t.Fatal(err)
	}
	hostSync := make(chan error, 1)
	go func() {
		_, err := host.SyncWorkspace(ctx, hostWorkspace, "Sprint Notes")
		hostSync <- err
	}()
	if _, err := join.SyncWorkspace(ctx, joinWorkspace, "Sprint Notes"); err != nil {
		t.Fatal(err)
	}
	if err := <-hostSync; err != nil {
		t.Fatal(err)
	}

	hostDocument := hostWorkspace.Snapshot("Sprint Notes")
	joinDocument := joinWorkspace.Snapshot("Sprint Notes")
	if hostDocument != joinDocument || hostDocument.Text != "join draft" {
		t.Fatalf("workspaces did not converge: host=%#v join=%#v", hostDocument, joinDocument)
	}
}

func TestWorkspaceSyncCapabilityFallsBackForLegacyPeer(t *testing.T) {
	hostTransport, legacyTransport := newQueuedPair()
	defer hostTransport.Close()
	defer legacyTransport.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	type sessionResult struct {
		session *Session
		err     error
	}
	hostResult := make(chan sessionResult, 1)
	go func() {
		session, err := NewHost(ctx, hostTransport, "ABC123")
		hostResult <- sessionResult{session: session, err: err}
	}()
	helloPayload, err := legacyTransport.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var hello map[string]any
	if err := json.Unmarshal(helloPayload, &hello); err != nil {
		t.Fatal(err)
	}
	if hello["workspace_sync"] != true {
		t.Fatalf("host hello = %#v", hello)
	}
	ackPayload, err := json.Marshal(map[string]any{
		"type":           "hello_ack",
		"version":        ProtocolVersion,
		"receiver_nonce": "legacy-receiver",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacyTransport.Send(ctx, ackPayload); err != nil {
		t.Fatal(err)
	}
	result := <-hostResult
	if result.err != nil {
		t.Fatal(result.err)
	}
	defer result.session.Close()
	if result.session.WorkspaceSyncSupported() {
		t.Fatal("legacy acknowledgement unexpectedly enabled workspace sync")
	}
}

func TestRunInteractivePublishesAndClears(t *testing.T) {
	left, right := net.Pipe()
	hostTransport := transport.NewTCPTransport(left)
	joinTransport := transport.NewTCPTransport(right)
	defer hostTransport.Close()
	defer joinTransport.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	type sessionResult struct {
		session *Session
		err     error
	}
	hostResult := make(chan sessionResult, 1)
	go func() {
		session, err := NewHost(ctx, hostTransport, "ABC123")
		hostResult <- sessionResult{session: session, err: err}
	}()
	join, err := NewJoin(ctx, joinTransport, "ABC123")
	if err != nil {
		t.Fatal(err)
	}
	hostResultValue := <-hostResult
	if hostResultValue.err != nil {
		t.Fatal(hostResultValue.err)
	}
	host := hostResultValue.session
	defer host.Close()
	defer join.Close()

	input := strings.NewReader("hello\n/clear\n/quit\n")
	var output bytes.Buffer
	done := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		done <- RunInteractive(ctx, host, InteractiveOptions{
			In:      input,
			Out:     &output,
			OnReady: func() { close(ready) },
		})
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("interactive session did not become ready")
	}
	remote, err := join.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if remote.Type != FrameUpdate || remote.Text != "hello" {
		t.Fatalf("first frame = %#v", remote)
	}
	joinAck := make(chan error, 1)
	go func() {
		joinAck <- join.Send(ctx, Frame{
			Type:     FrameAck,
			Version:  ProtocolVersion,
			Pad:      DefaultPad,
			Revision: remote.Revision,
		})
	}()
	clearFrame, err := join.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if clearFrame.Type != FrameClear || clearFrame.Text != "" {
		t.Fatalf("clear frame = %#v", clearFrame)
	}
	if err := <-joinAck; err != nil {
		t.Fatal(err)
	}
	joinAck = make(chan error, 1)
	go func() {
		joinAck <- join.Send(ctx, Frame{
			Type:     FrameAck,
			Version:  ProtocolVersion,
			Pad:      DefaultPad,
			Revision: clearFrame.Revision,
		})
	}()
	if _, err := join.Recv(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-joinAck; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Sent revision 1") ||
		!strings.Contains(output.String(), "Sent clear revision 2") {
		t.Fatalf("output = %q", output.String())
	}
}

type queuedTransport struct {
	incoming chan []byte
	outgoing chan []byte
	closed   chan struct{}
	once     sync.Once
}

func newQueuedPair() (*queuedTransport, *queuedTransport) {
	leftToRight := make(chan []byte, 16)
	rightToLeft := make(chan []byte, 16)
	return &queuedTransport{
			incoming: rightToLeft,
			outgoing: leftToRight,
			closed:   make(chan struct{}),
		},
		&queuedTransport{
			incoming: leftToRight,
			outgoing: rightToLeft,
			closed:   make(chan struct{}),
		}
}

func (t *queuedTransport) Send(ctx context.Context, payload []byte) error {
	select {
	case t.outgoing <- append([]byte(nil), payload...):
		return nil
	case <-t.closed:
		return transport.ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *queuedTransport) Recv(ctx context.Context) ([]byte, error) {
	select {
	case payload := <-t.incoming:
		return append([]byte(nil), payload...), nil
	case <-t.closed:
		return nil, transport.ErrClosed
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (t *queuedTransport) Close() error {
	t.once.Do(func() {
		close(t.closed)
	})
	return nil
}
