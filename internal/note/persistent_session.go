package note

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/suir1/kigo/internal/secure"
	"github.com/suir1/kigo/internal/transport"
)

const persistentWriteWait = 10 * time.Second

type PersistentOptions struct {
	ServiceBase string
	Code        string
	Pad         string
	Dialer      *websocket.Dialer
}

type PersistentSession struct {
	conn       *websocket.Conn
	code       string
	pad        string
	initial    PersistentMessage
	events     chan persistentSessionEvent
	done       chan struct{}
	workspace  *Workspace
	generation uint64
	mu         sync.Mutex
	writeMu    sync.Mutex
	startOnce  sync.Once
	closeOnce  sync.Once
}

type persistentSessionEvent struct {
	frame Frame
	err   error
}

func OpenPersistentSession(ctx context.Context, options PersistentOptions) (*PersistentSession, error) {
	code, err := secure.ValidateCode(options.Code)
	if err != nil {
		return nil, err
	}
	pad := NormalizePad(options.Pad)
	if err := ValidatePad(pad); err != nil {
		return nil, err
	}
	endpoint, err := PersistentEndpoint(options.ServiceBase, code, pad)
	if err != nil {
		return nil, err
	}
	dialer := options.Dialer
	if dialer == nil {
		copy := *websocket.DefaultDialer
		dialer = &copy
	}
	conn, _, err := dialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(MaxPersistentMessageSize)
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetReadDeadline(deadline)
	} else {
		_ = conn.SetReadDeadline(time.Now().Add(persistentWriteWait))
	}
	var initial PersistentMessage
	if err := conn.ReadJSON(&initial); err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Time{})
	if err := validatePersistentState(initial); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &PersistentSession{
		conn:       conn,
		code:       code,
		pad:        pad,
		initial:    initial,
		events:     make(chan persistentSessionEvent, 32),
		done:       make(chan struct{}),
		generation: initial.Generation,
	}, nil
}

func PersistentEndpoint(serviceBase, code, pad string) (string, error) {
	base, err := url.Parse(strings.TrimSpace(serviceBase))
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return "", errors.New("persistent notepad service must be an HTTP(S) origin")
	}
	switch strings.ToLower(base.Scheme) {
	case "http":
		base.Scheme = "ws"
	case "https":
		base.Scheme = "wss"
	default:
		return "", errors.New("persistent notepad service must use HTTP(S)")
	}
	code, err = secure.ValidateCode(code)
	if err != nil {
		return "", err
	}
	padToken, err := PersistentPadToken(pad)
	if err != nil {
		return "", err
	}
	base.Path = strings.TrimRight(base.Path, "/") + "/api/note-sync/" + secure.RoomToken(code) + "/" + padToken
	return base.String(), nil
}

func (s *PersistentSession) SyncWorkspace(ctx context.Context, workspace *Workspace, pad string) ([]Document, error) {
	if s == nil || s.conn == nil {
		return nil, transport.ErrClosed
	}
	pad = NormalizePad(pad)
	if pad != s.pad {
		return nil, fmt.Errorf("persistent notepad selected pad %q, local pad is %q", s.pad, pad)
	}
	if workspace == nil {
		workspace = NewWorkspace()
	}
	s.mu.Lock()
	s.workspace = workspace
	initial := s.initial
	s.initial = PersistentMessage{}
	s.mu.Unlock()

	var applied []Document
	if initial.Record != nil {
		document, err := OpenPersistentDocument(s.code, s.pad, *initial.Record)
		if err != nil {
			return nil, err
		}
		changed, current, err := workspace.ApplyRemote(document)
		if err != nil {
			return nil, err
		}
		if changed {
			applied = append(applied, current)
		}
	}
	current := workspace.Snapshot(s.pad)
	if current.Revision > 0 && (initial.Record == nil || len(applied) == 0) {
		if err := s.publishDocument(ctx, current); err != nil {
			return nil, err
		}
	}
	s.start()
	return applied, nil
}

func (s *PersistentSession) Send(ctx context.Context, frame Frame) error {
	if s == nil || s.conn == nil {
		return transport.ErrClosed
	}
	frame.Version = ProtocolVersion
	if frame.Pad == "" {
		frame.Pad = s.pad
	}
	if err := frame.Validate(); err != nil {
		return err
	}
	switch frame.Type {
	case FrameUpdate, FrameClear:
		return s.publishDocument(ctx, frame.Document())
	case FrameBye:
		return s.Close()
	case FrameAck, FramePing, FramePong:
		return nil
	default:
		return fmt.Errorf("unsupported persistent notepad frame %q", frame.Type)
	}
}

func (s *PersistentSession) Recv(ctx context.Context) (Frame, error) {
	if s == nil {
		return Frame{}, transport.ErrClosed
	}
	s.start()
	select {
	case <-ctx.Done():
		return Frame{}, ctx.Err()
	case event := <-s.events:
		return event.frame, event.err
	case <-s.done:
		return Frame{}, transport.ErrClosed
	}
}

func (s *PersistentSession) Close() error {
	if s == nil {
		return nil
	}
	var err error
	s.closeOnce.Do(func() {
		close(s.done)
		if s.conn != nil {
			err = s.conn.Close()
		}
	})
	return err
}

func (s *PersistentSession) start() {
	if s == nil {
		return
	}
	s.startOnce.Do(func() { go s.readLoop() })
}

func (s *PersistentSession) readLoop() {
	for {
		var message PersistentMessage
		if err := s.conn.ReadJSON(&message); err != nil {
			s.emit(persistentSessionEvent{err: err})
			return
		}
		if message.Type == PersistentError {
			s.emit(persistentSessionEvent{err: errors.New(message.Error)})
			continue
		}
		if err := validatePersistentState(message); err != nil {
			s.emit(persistentSessionEvent{err: err})
			continue
		}
		s.mu.Lock()
		if message.Generation < s.generation {
			s.mu.Unlock()
			continue
		}
		s.generation = message.Generation
		workspace := s.workspace
		s.mu.Unlock()
		if workspace == nil {
			continue
		}
		if message.Record == nil {
			current := workspace.Snapshot(s.pad)
			if current.Revision > 0 {
				if err := s.publishDocument(context.Background(), current); err != nil {
					s.emit(persistentSessionEvent{err: err})
				}
			}
			continue
		}
		document, err := OpenPersistentDocument(s.code, s.pad, *message.Record)
		if err != nil {
			s.emit(persistentSessionEvent{err: err})
			continue
		}
		current := workspace.Snapshot(s.pad)
		switch compareDocuments(document, current) {
		case 1:
			s.emit(persistentSessionEvent{frame: FrameFromDocument(frameTypeForDocument(document), document)})
		case -1:
			if err := s.publishDocument(context.Background(), current); err != nil {
				s.emit(persistentSessionEvent{err: err})
			}
		default:
			s.emit(persistentSessionEvent{frame: Frame{
				Type: FrameAck, Version: ProtocolVersion, Pad: s.pad, Revision: document.Revision, Timestamp: document.Timestamp,
			}})
		}
	}
}

func (s *PersistentSession) publishDocument(ctx context.Context, document Document) error {
	document.Pad = NormalizePad(document.Pad)
	if document.Pad != s.pad {
		return fmt.Errorf("persistent notepad selected pad %q, update uses %q", s.pad, document.Pad)
	}
	record, err := SealPersistentDocument(s.code, document)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.mu.Lock()
	generation := s.generation
	s.mu.Unlock()
	deadline := time.Now().Add(persistentWriteWait)
	if value, ok := ctx.Deadline(); ok && value.Before(deadline) {
		deadline = value
	}
	_ = s.conn.SetWriteDeadline(deadline)
	return s.conn.WriteJSON(PersistentMessage{
		Type: PersistentPut, Version: PersistentProtocolVersion, BaseGeneration: generation, Record: &record,
	})
}

func (s *PersistentSession) emit(event persistentSessionEvent) {
	select {
	case s.events <- event:
	case <-s.done:
	}
}

func validatePersistentState(message PersistentMessage) error {
	if message.Version != PersistentProtocolVersion {
		return fmt.Errorf("unsupported persistent note protocol version %d", message.Version)
	}
	if message.Type == PersistentError {
		return errors.New(message.Error)
	}
	if message.Type != PersistentState {
		return fmt.Errorf("unsupported persistent note message type %q", message.Type)
	}
	if message.Record != nil {
		return message.Record.Validate()
	}
	if message.Generation != 0 {
		return errors.New("persistent note state is missing its record")
	}
	return nil
}

func frameTypeForDocument(document Document) string {
	if document.Text == "" {
		return FrameClear
	}
	return FrameUpdate
}

var _ interface {
	Send(context.Context, Frame) error
	Recv(context.Context) (Frame, error)
	SyncWorkspace(context.Context, *Workspace, string) ([]Document, error)
	Close() error
} = (*PersistentSession)(nil)
