package note

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/suir1/kigo/internal/secure"
	"github.com/suir1/kigo/internal/transport"
)

const (
	sessionInfoHostToJoin = "kigo-note-v1:host-to-join"
	sessionInfoJoinToHost = "kigo-note-v1:join-to-host"
	maxEnvelopeSize       = 4 << 20
)

type Session struct {
	t             transport.Transport
	send          *secure.Session
	recv          *secure.Session
	sendMu        sync.Mutex
	sendSeq       uint64
	recvSeq       uint64
	host          bool
	workspaceSync bool
	closed        bool
	closeMu       sync.RWMutex
}

type handshakeMessage struct {
	Type          string `json:"type"`
	Version       int    `json:"version"`
	SenderNonce   string `json:"sender_nonce,omitempty"`
	ReceiverNonce string `json:"receiver_nonce,omitempty"`
	WorkspaceSync bool   `json:"workspace_sync,omitempty"`
}

type envelope struct {
	Version    int    `json:"version"`
	Sequence   uint64 `json:"seq"`
	Ciphertext []byte `json:"ciphertext"`
}

func NewHost(ctx context.Context, t transport.Transport, code string) (*Session, error) {
	return newSession(ctx, t, code, true)
}

func NewJoin(ctx context.Context, t transport.Transport, code string) (*Session, error) {
	return newSession(ctx, t, code, false)
}

func newSession(ctx context.Context, t transport.Transport, code string, host bool) (*Session, error) {
	if t == nil {
		return nil, transport.ErrClosed
	}
	code, err := secure.ValidateCode(code)
	if err != nil {
		return nil, err
	}
	channel := transport.Channels(t)
	if len(channel) == 0 || channel[0] == nil {
		return nil, transport.ErrClosed
	}
	pipe := channel[0]
	var senderNonce, receiverNonce string
	workspaceSync := false
	if host {
		senderNonce, err = secure.RandomNonce()
		if err != nil {
			return nil, err
		}
		if err := sendPlain(ctx, pipe, handshakeMessage{
			Type:          "hello",
			Version:       ProtocolVersion,
			SenderNonce:   senderNonce,
			WorkspaceSync: true,
		}); err != nil {
			return nil, err
		}
		var ack handshakeMessage
		if err := recvPlain(ctx, pipe, &ack); err != nil {
			return nil, err
		}
		if err := validateHandshake(ack, "hello_ack"); err != nil {
			return nil, err
		}
		receiverNonce = ack.ReceiverNonce
		workspaceSync = ack.WorkspaceSync
	} else {
		var hello handshakeMessage
		if err := recvPlain(ctx, pipe, &hello); err != nil {
			return nil, err
		}
		if err := validateHandshake(hello, "hello"); err != nil {
			return nil, err
		}
		senderNonce = hello.SenderNonce
		receiverNonce, err = secure.RandomNonce()
		if err != nil {
			return nil, err
		}
		if err := sendPlain(ctx, pipe, handshakeMessage{
			Type:          "hello_ack",
			Version:       ProtocolVersion,
			ReceiverNonce: receiverNonce,
			WorkspaceSync: hello.WorkspaceSync,
		}); err != nil {
			return nil, err
		}
		workspaceSync = hello.WorkspaceSync
	}
	sendInfo := sessionInfoHostToJoin
	recvInfo := sessionInfoJoinToHost
	if !host {
		sendInfo, recvInfo = recvInfo, sendInfo
	}
	sendSession, err := secure.NewSessionWithInfo(code, senderNonce, receiverNonce, sendInfo)
	if err != nil {
		return nil, err
	}
	recvSession, err := secure.NewSessionWithInfo(code, senderNonce, receiverNonce, recvInfo)
	if err != nil {
		return nil, err
	}
	return &Session{
		t:             pipe,
		send:          sendSession,
		recv:          recvSession,
		host:          host,
		workspaceSync: workspaceSync,
	}, nil
}

func (s *Session) Send(ctx context.Context, frame Frame) error {
	if s == nil {
		return transport.ErrClosed
	}
	frame.Version = ProtocolVersion
	if frame.Pad == "" && frame.IsDocumentUpdate() {
		frame.Pad = DefaultPad
	}
	if err := frame.Validate(); err != nil {
		return err
	}
	body, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.isClosed() {
		return transport.ErrClosed
	}
	s.sendSeq++
	sequence := s.sendSeq
	ciphertext, err := s.send.Encrypt(sequence, body)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(envelope{
		Version:    ProtocolVersion,
		Sequence:   sequence,
		Ciphertext: ciphertext,
	})
	if err != nil {
		return err
	}
	if len(payload) > maxEnvelopeSize {
		return fmt.Errorf("note envelope exceeds %d bytes", maxEnvelopeSize)
	}
	return s.t.Send(ctx, payload)
}

func (s *Session) Recv(ctx context.Context) (Frame, error) {
	if s == nil {
		return Frame{}, transport.ErrClosed
	}
	if s.isClosed() {
		return Frame{}, transport.ErrClosed
	}
	payload, err := s.t.Recv(ctx)
	if err != nil {
		return Frame{}, err
	}
	if len(payload) > maxEnvelopeSize {
		return Frame{}, fmt.Errorf("note envelope exceeds %d bytes", maxEnvelopeSize)
	}
	var message envelope
	if err := json.Unmarshal(payload, &message); err != nil {
		return Frame{}, fmt.Errorf("decode note envelope: %w", err)
	}
	if message.Version != ProtocolVersion {
		return Frame{}, fmt.Errorf("unsupported note envelope version %d", message.Version)
	}
	if message.Sequence != s.recvSeq+1 {
		return Frame{}, fmt.Errorf("unexpected note envelope sequence %d, want %d", message.Sequence, s.recvSeq+1)
	}
	body, err := s.recv.Decrypt(message.Sequence, message.Ciphertext)
	if err != nil {
		return Frame{}, fmt.Errorf("decrypt note frame: %w", err)
	}
	var frame Frame
	if err := json.Unmarshal(body, &frame); err != nil {
		return Frame{}, fmt.Errorf("decode note frame: %w", err)
	}
	if err := frame.Validate(); err != nil {
		return Frame{}, err
	}
	s.recvSeq = message.Sequence
	return frame, nil
}

func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.closeMu.Lock()
	if s.closed {
		s.closeMu.Unlock()
		return nil
	}
	s.closed = true
	s.closeMu.Unlock()
	return s.t.Close()
}

func (s *Session) Host() bool {
	return s != nil && s.host
}

func (s *Session) WorkspaceSyncSupported() bool {
	return s != nil && s.workspaceSync
}

func (s *Session) isClosed() bool {
	s.closeMu.RLock()
	defer s.closeMu.RUnlock()
	return s.closed
}

func validateHandshake(message handshakeMessage, expectedType string) error {
	if message.Type != expectedType {
		return fmt.Errorf("expected note %s, got %q", expectedType, message.Type)
	}
	if message.Version != ProtocolVersion {
		return fmt.Errorf("unsupported note handshake version %d", message.Version)
	}
	switch expectedType {
	case "hello":
		if message.SenderNonce == "" {
			return errors.New("note hello is missing sender nonce")
		}
	case "hello_ack":
		if message.ReceiverNonce == "" {
			return errors.New("note hello acknowledgement is missing receiver nonce")
		}
	}
	return nil
}

func sendPlain(ctx context.Context, t transport.Transport, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return t.Send(ctx, payload)
}

func recvPlain(ctx context.Context, t transport.Transport, value any) error {
	payload, err := t.Recv(ctx)
	if err != nil {
		return err
	}
	if len(payload) > maxEnvelopeSize {
		return fmt.Errorf("note handshake exceeds %d bytes", maxEnvelopeSize)
	}
	if err := json.Unmarshal(payload, value); err != nil {
		return fmt.Errorf("decode note handshake: %w", err)
	}
	return nil
}

func (s *Session) String() string {
	if s == nil {
		return "note session <nil>"
	}
	role := "join"
	if s.host {
		role = "host"
	}
	return fmt.Sprintf("note session %s", role)
}

var _ io.Closer = (*Session)(nil)
