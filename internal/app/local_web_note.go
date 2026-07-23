package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/suir1/kigo/internal/note"
	"github.com/suir1/kigo/internal/secure"
	"github.com/suir1/kigo/internal/transport"
	"github.com/suir1/kigo/internal/transport/webrtcx"
)

const (
	localWebNoteSendTimeout  = 10 * time.Second
	localWebNoteLeaveTimeout = 250 * time.Millisecond
)

type localWebNoteSnapshot struct {
	ID             uint64 `json:"id"`
	Running        bool   `json:"running"`
	Connected      bool   `json:"connected"`
	Host           bool   `json:"host"`
	Code           string `json:"code,omitempty"`
	Pad            string `json:"pad"`
	Link           string `json:"link,omitempty"`
	Text           string `json:"text"`
	Revision       uint64 `json:"revision"`
	AckedRevision  uint64 `json:"acked_revision"`
	Synced         bool   `json:"synced"`
	Status         string `json:"status"`
	Error          string `json:"error,omitempty"`
	DraftRecovered bool   `json:"draft_recovered,omitempty"`
	DraftWarning   string `json:"draft_warning,omitempty"`
	StartedAt      int64  `json:"started_at,omitempty"`
	FinishedAt     int64  `json:"finished_at,omitempty"`
}

type localWebNotePeer interface {
	Send(context.Context, note.Frame) error
	Recv(context.Context) (note.Frame, error)
	Close() error
}

type localWebNoteConnectFunc func(context.Context, string, bool) (localWebNotePeer, error)

type localWebNoteConnection struct {
	connect      localWebNoteConnectFunc
	retryAllowed func() bool
}

type localWebNoteConnectionFactory func(string, bool) localWebNoteConnection

type localWebNoteWorkspaceSyncer interface {
	SyncWorkspace(context.Context, *note.Workspace, string) ([]note.Document, error)
}

type localWebNoteStore struct {
	parent            context.Context
	connectFor        localWebNoteConnectionFactory
	reconnectAttempts int
	reconnectDelay    time.Duration
	reconnectErr      error
	drafts            *note.DraftStore
	generateCode      func() (string, error)
	linkForCode       func(string, string) string

	mu              sync.Mutex
	sendMu          sync.Mutex
	sequence        uint64
	state           localWebNoteSnapshot
	workspace       *note.Workspace
	session         localWebNotePeer
	sessionCtx      context.Context
	cancel          context.CancelFunc
	pendingRevision uint64
}

func newLocalWebNoteStore(parent context.Context, g *globalOptions) *localWebNoteStore {
	store := newLocalWebNoteStoreWithConnector(parent, g, nil)
	if g == nil || !g.NoNoteDrafts {
		store.drafts = note.NewDraftStore(noteDraftPath())
	}
	connectionOptions := cloneGlobalOptions(g)
	connectionOptions.taskOutput = newClientTaskOutput(io.Discard, io.Discard, func(nativeTaskEvent) {})
	store.connectFor = func(code string, host bool) localWebNoteConnection {
		return localWebNoteConnector(connectionOptions, code, host)
	}
	return store
}

func newLocalWebNoteStoreWithConnector(
	parent context.Context,
	g *globalOptions,
	connect localWebNoteConnectFunc,
) *localWebNoteStore {
	if parent == nil {
		parent = context.Background()
	}
	attempts, delay, reconnectErr := localWebNoteReconnectConfig(g)
	return &localWebNoteStore{
		parent:            parent,
		reconnectAttempts: attempts,
		reconnectDelay:    delay,
		reconnectErr:      reconnectErr,
		generateCode:      secure.GenerateCode,
		connectFor: func(string, bool) localWebNoteConnection {
			return localWebNoteConnection{
				connect:      connect,
				retryAllowed: func() bool { return true },
			}
		},
		linkForCode: func(code, pad string) string {
			return notePublicLink(g, code, pad)
		},
		state: localWebNoteSnapshot{Pad: note.DefaultPad, Status: "idle"},
	}
}

func localWebNoteReconnectConfig(g *globalOptions) (int, time.Duration, error) {
	if g == nil || g.NoReconnect || g.ReconnectAttempts == 0 {
		return 1, 0, nil
	}
	if g.ReconnectAttempts < 1 {
		return 0, 0, errors.New("reconnect attempts must be at least 1")
	}
	if g.ReconnectDelay < 0 {
		return 0, 0, errors.New("reconnect delay cannot be negative")
	}
	return g.ReconnectAttempts, g.ReconnectDelay, nil
}

func localWebNoteConnector(g *globalOptions, code string, host bool) localWebNoteConnection {
	role := "receiver"
	if host {
		role = "sender"
	}
	pairing := newPairingWindow(g)
	reconnect := &webrtcx.ReconnectState{}
	var (
		resolveOnce sync.Once
		options     *globalOptions
		resolveErr  error
	)
	dial := pairing.dialer(func(pairCtx context.Context) (transport.Transport, error) {
		return dialTransport(pairCtx, options, secure.RoomToken(code), role, reconnect)
	})
	connect := func(ctx context.Context, _ string, _ bool) (localWebNotePeer, error) {
		resolveOnce.Do(func() {
			options, resolveErr = withPairingWindow(ctx, pairing, func(pairCtx context.Context) (*globalOptions, error) {
				return resolveNoteOptions(pairCtx, g, secure.RoomToken(code), role)
			})
		})
		if resolveErr != nil {
			return nil, resolveErr
		}
		connection, err := dial(ctx)
		if err != nil {
			return nil, err
		}
		var session *note.Session
		if host {
			session, err = note.NewHost(ctx, connection, code)
		} else {
			session, err = note.NewJoin(ctx, connection, code)
		}
		if err != nil {
			_ = connection.Close()
			return nil, err
		}
		return session, nil
	}
	return localWebNoteConnection{
		connect: connect,
		retryAllowed: func() bool {
			return options != nil && webRTCReconnectAllowed(options, reconnect)
		},
	}
}

func (s *localWebNoteStore) StartHost() (localWebNoteSnapshot, error) {
	return s.StartHostWithCodeAndPad("", note.DefaultPad)
}

func (s *localWebNoteStore) StartHostWithCode(requested string) (localWebNoteSnapshot, error) {
	return s.StartHostWithCodeAndPad(requested, note.DefaultPad)
}

func (s *localWebNoteStore) StartHostWithCodeAndPad(requested, pad string) (localWebNoteSnapshot, error) {
	if s == nil {
		return localWebNoteSnapshot{}, errors.New("local notepad is unavailable")
	}
	var code string
	var err error
	if strings.TrimSpace(requested) == "" {
		if s.generateCode == nil {
			return localWebNoteSnapshot{}, errors.New("local notepad is unavailable")
		}
		code, err = s.generateCode()
	} else {
		code, err = secure.ValidateCode(requested)
	}
	if err != nil {
		return localWebNoteSnapshot{}, err
	}
	return s.start(code, true, pad)
}

func (s *localWebNoteStore) StartJoin(code string) (localWebNoteSnapshot, error) {
	return s.StartJoinPad(code, note.DefaultPad)
}

func (s *localWebNoteStore) StartJoinPad(code, pad string) (localWebNoteSnapshot, error) {
	return s.start(code, false, pad)
}

func (s *localWebNoteStore) start(code string, host bool, pad string) (localWebNoteSnapshot, error) {
	if s == nil || s.connectFor == nil {
		return localWebNoteSnapshot{}, errors.New("local notepad is unavailable")
	}
	if s.reconnectErr != nil {
		return localWebNoteSnapshot{}, s.reconnectErr
	}
	code, err := secure.ValidateCode(code)
	if err != nil {
		return localWebNoteSnapshot{}, err
	}
	pad = note.NormalizePad(pad)
	if err := note.ValidatePad(pad); err != nil {
		return localWebNoteSnapshot{}, err
	}
	workspace := note.NewWorkspace()
	draftRecovered := false
	draftWarning := ""
	if document, ok, loadErr := loadNoteDraft(s.drafts, code, host, pad); loadErr != nil {
		draftWarning = loadErr.Error()
	} else if ok {
		if _, _, applyErr := workspace.ApplyRemote(document); applyErr != nil {
			draftWarning = applyErr.Error()
		} else {
			draftRecovered = true
		}
	}
	document := workspace.Snapshot(pad)

	s.mu.Lock()
	if s.state.Running {
		s.mu.Unlock()
		return localWebNoteSnapshot{}, errors.New("a notepad session is already running")
	}
	if err := s.parent.Err(); err != nil {
		s.mu.Unlock()
		return localWebNoteSnapshot{}, err
	}
	s.sequence++
	id := s.sequence
	ctx, cancel := context.WithCancel(s.parent)
	status := "connecting"
	if host {
		status = "waiting"
	}
	link := ""
	if host && s.linkForCode != nil {
		link = s.linkForCode(code, pad)
	}
	s.workspace = workspace
	s.session = nil
	s.sessionCtx = ctx
	s.cancel = cancel
	s.pendingRevision = 0
	s.state = localWebNoteSnapshot{
		ID:             id,
		Running:        true,
		Host:           host,
		Code:           code,
		Pad:            pad,
		Link:           link,
		Text:           document.Text,
		Revision:       document.Revision,
		Status:         status,
		DraftRecovered: draftRecovered,
		DraftWarning:   draftWarning,
		StartedAt:      time.Now().UnixMilli(),
	}
	snapshot := s.state
	s.mu.Unlock()

	connection := s.connectFor(code, host)
	if connection.connect == nil {
		cancel()
		s.fail(id, errors.New("local notepad is unavailable"))
		return localWebNoteSnapshot{}, errors.New("local notepad is unavailable")
	}
	go s.run(id, ctx, code, host, pad, workspace, connection)
	return snapshot, nil
}

func (s *localWebNoteStore) run(
	id uint64,
	ctx context.Context,
	code string,
	host bool,
	pad string,
	workspace *note.Workspace,
	connection localWebNoteConnection,
) {
	for attempt := 1; attempt <= s.reconnectAttempts; attempt++ {
		session, err := connection.connect(ctx, code, host)
		if err == nil {
			if syncer, ok := session.(localWebNoteWorkspaceSyncer); ok {
				_, err = syncer.SyncWorkspace(ctx, workspace, pad)
			}
			if err == nil {
				s.persistDraft(id, workspace.Snapshot(pad))
			}
		}
		if err == nil && !s.setConnected(id, session) {
			_ = session.Close()
			return
		}
		if err == nil {
			err = s.receiveSession(id, ctx, session, pad)
			_ = session.Close()
			if err == nil {
				return
			}
		}
		if ctx.Err() != nil {
			s.finishCanceled(id)
			return
		}
		if attempt >= s.reconnectAttempts || !isRetryableTransferError(err) ||
			(connection.retryAllowed != nil && !connection.retryAllowed()) {
			s.fail(id, err)
			return
		}
		if !s.setReconnecting(id) {
			return
		}
		timer := time.NewTimer(s.reconnectDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			s.finishCanceled(id)
			return
		case <-timer.C:
		}
	}
}

func (s *localWebNoteStore) receiveSession(
	id uint64,
	ctx context.Context,
	session localWebNotePeer,
	pad string,
) error {
	for {
		frame, err := session.Recv(ctx)
		if err != nil {
			return err
		}
		if err := s.handleFrame(id, ctx, session, frame, pad); err != nil {
			return err
		}
		if frame.Type == note.FrameBye {
			return nil
		}
	}
}

func (s *localWebNoteStore) setConnected(id uint64, session localWebNotePeer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.ID != id || !s.state.Running {
		return false
	}
	s.session = session
	document := s.workspace.Snapshot(s.state.Pad)
	s.state.Connected = true
	s.state.Synced = true
	s.state.Status = "connected"
	s.state.Text = document.Text
	s.state.Revision = document.Revision
	s.state.AckedRevision = document.Revision
	s.pendingRevision = 0
	return true
}

func (s *localWebNoteStore) setReconnecting(id uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.ID != id || !s.state.Running {
		return false
	}
	s.state.Connected = false
	s.state.Synced = false
	s.state.Status = "reconnecting"
	s.session = nil
	return true
}

func (s *localWebNoteStore) handleFrame(
	id uint64,
	ctx context.Context,
	session localWebNotePeer,
	frame note.Frame,
	pad string,
) error {
	if note.NormalizePad(frame.Pad) != pad {
		return fmt.Errorf("peer selected note pad %q, local pad is %q", frame.Pad, pad)
	}
	switch frame.Type {
	case note.FrameUpdate, note.FrameClear:
		s.sendMu.Lock()
		defer s.sendMu.Unlock()
		workspace, ok := s.activeWorkspace(id, session)
		if !ok {
			return context.Canceled
		}
		applied, document, err := workspace.ApplyRemote(frame.Document())
		if err != nil {
			return err
		}
		if applied {
			s.persistDraft(id, document)
		}
		s.mu.Lock()
		if s.state.ID == id && s.state.Running {
			s.state.Text = document.Text
			s.state.Revision = document.Revision
			if applied {
				s.pendingRevision = 0
				s.state.Synced = true
				s.state.Status = "synced"
			}
		}
		s.mu.Unlock()
		return sendLocalWebNoteFrame(ctx, session, note.Frame{
			Type:      note.FrameAck,
			Version:   note.ProtocolVersion,
			Pad:       pad,
			Revision:  document.Revision,
			Timestamp: document.Timestamp,
		})
	case note.FrameAck:
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.state.ID != id || !s.state.Running {
			return context.Canceled
		}
		if frame.Revision > s.state.AckedRevision {
			s.state.AckedRevision = frame.Revision
		}
		if s.pendingRevision != 0 && frame.Revision >= s.pendingRevision {
			s.pendingRevision = 0
			s.state.Synced = true
			s.state.Status = "synced"
		}
		return nil
	case note.FramePing:
		s.sendMu.Lock()
		defer s.sendMu.Unlock()
		if _, ok := s.activeWorkspace(id, session); !ok {
			return context.Canceled
		}
		return sendLocalWebNoteFrame(ctx, session, note.Frame{
			Type:    note.FramePong,
			Version: note.ProtocolVersion,
			Pad:     pad,
		})
	case note.FramePong:
		return nil
	case note.FrameBye:
		s.peerLeft(id)
		return nil
	default:
		return fmt.Errorf("unsupported note frame type %q", frame.Type)
	}
}

func (s *localWebNoteStore) activeWorkspace(
	id uint64,
	session localWebNotePeer,
) (*note.Workspace, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.ID != id || !s.state.Running || s.session != session || s.workspace == nil {
		return nil, false
	}
	return s.workspace, true
}

func (s *localWebNoteStore) Update(text string) (localWebNoteSnapshot, error) {
	if err := note.ValidateText(text); err != nil {
		return localWebNoteSnapshot{}, err
	}
	return s.publish(note.FrameUpdate, text)
}

func (s *localWebNoteStore) Clear() (localWebNoteSnapshot, error) {
	return s.publish(note.FrameClear, "")
}

func (s *localWebNoteStore) publish(frameType, text string) (localWebNoteSnapshot, error) {
	if s == nil {
		return localWebNoteSnapshot{}, errors.New("local notepad is unavailable")
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()

	s.mu.Lock()
	if !s.state.Running || !s.state.Connected || s.session == nil || s.workspace == nil {
		s.mu.Unlock()
		return localWebNoteSnapshot{}, errors.New("notepad is not connected")
	}
	id := s.state.ID
	session := s.session
	sessionCtx := s.sessionCtx
	workspace := s.workspace
	pad := s.state.Pad
	s.mu.Unlock()

	var (
		document note.Document
		err      error
	)
	if frameType == note.FrameClear {
		document, err = workspace.Clear(pad, time.Now())
	} else {
		document, err = workspace.Update(pad, text, time.Now())
	}
	if err != nil {
		return localWebNoteSnapshot{}, err
	}
	s.persistDraft(id, document)

	s.mu.Lock()
	if s.state.ID != id || !s.state.Running || s.session != session {
		s.mu.Unlock()
		return localWebNoteSnapshot{}, errors.New("notepad session changed")
	}
	s.state.Text = document.Text
	s.state.Revision = document.Revision
	s.state.Synced = false
	s.state.Status = "syncing"
	s.pendingRevision = document.Revision
	snapshot := s.state
	s.mu.Unlock()

	if err := sendLocalWebNoteFrame(
		sessionCtx,
		session,
		note.FrameFromDocument(frameType, document),
	); err != nil {
		_ = session.Close()
		return localWebNoteSnapshot{}, err
	}
	return snapshot, nil
}

func (s *localWebNoteStore) persistDraft(id uint64, document note.Document) {
	if s == nil || s.drafts == nil || document.Revision == 0 {
		return
	}
	s.mu.Lock()
	if s.state.ID != id || !s.state.Running {
		s.mu.Unlock()
		return
	}
	code := s.state.Code
	host := s.state.Host
	s.mu.Unlock()
	if err := saveNoteDraft(s.drafts, code, host, document); err != nil {
		s.mu.Lock()
		if s.state.ID == id {
			s.state.DraftWarning = err.Error()
		}
		s.mu.Unlock()
	}
}

func sendLocalWebNoteFrame(
	parent context.Context,
	session localWebNotePeer,
	frame note.Frame,
) error {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, localWebNoteSendTimeout)
	defer cancel()
	return session.Send(ctx, frame)
}

func (s *localWebNoteStore) Leave() localWebNoteSnapshot {
	if s == nil {
		return localWebNoteSnapshot{Pad: note.DefaultPad, Status: "idle"}
	}
	s.mu.Lock()
	if !s.state.Running {
		snapshot := s.state
		s.mu.Unlock()
		return snapshot
	}
	session := s.session
	pad := s.state.Pad
	cancel := s.cancel
	s.state.Running = false
	s.state.Connected = false
	s.state.Synced = false
	s.state.Status = "left"
	s.state.FinishedAt = time.Now().UnixMilli()
	s.session = nil
	s.sessionCtx = nil
	s.cancel = nil
	s.pendingRevision = 0
	snapshot := s.state
	s.mu.Unlock()

	if session != nil {
		ctx, stop := context.WithTimeout(context.Background(), localWebNoteLeaveTimeout)
		done := make(chan struct{})
		go func() {
			s.sendMu.Lock()
			_ = session.Send(ctx, note.Frame{
				Type:    note.FrameBye,
				Version: note.ProtocolVersion,
				Pad:     pad,
			})
			s.sendMu.Unlock()
			close(done)
		}()
		select {
		case <-done:
		case <-ctx.Done():
		}
		stop()
	}
	if cancel != nil {
		cancel()
	}
	if session != nil {
		_ = session.Close()
	}
	return snapshot
}

func (s *localWebNoteStore) Snapshot() localWebNoteSnapshot {
	if s == nil {
		return localWebNoteSnapshot{Pad: note.DefaultPad, Status: "idle"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *localWebNoteStore) peerLeft(id uint64) {
	s.mu.Lock()
	if s.state.ID != id || !s.state.Running {
		s.mu.Unlock()
		return
	}
	cancel := s.cancel
	s.state.Running = false
	s.state.Connected = false
	s.state.Synced = false
	s.state.Status = "peer_left"
	s.state.FinishedAt = time.Now().UnixMilli()
	s.session = nil
	s.sessionCtx = nil
	s.cancel = nil
	s.pendingRevision = 0
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *localWebNoteStore) finishCanceled(id uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.ID != id || !s.state.Running {
		return
	}
	s.state.Running = false
	s.state.Connected = false
	s.state.Synced = false
	s.state.Status = "left"
	s.state.FinishedAt = time.Now().UnixMilli()
	s.session = nil
	s.sessionCtx = nil
	s.cancel = nil
	s.pendingRevision = 0
}

func (s *localWebNoteStore) fail(id uint64, err error) {
	s.mu.Lock()
	if s.state.ID != id || !s.state.Running {
		s.mu.Unlock()
		return
	}
	cancel := s.cancel
	session := s.session
	s.state.Running = false
	s.state.Connected = false
	s.state.Synced = false
	s.state.Status = "error"
	s.state.Error = localWebNoteError(err)
	s.state.FinishedAt = time.Now().UnixMilli()
	s.session = nil
	s.sessionCtx = nil
	s.cancel = nil
	s.pendingRevision = 0
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if session != nil {
		_ = session.Close()
	}
}

func localWebNoteError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) {
		return "notepad session canceled"
	}
	if errors.Is(err, io.EOF) ||
		errors.Is(err, transport.ErrClosed) ||
		errors.Is(err, net.ErrClosed) {
		return "notepad connection closed"
	}
	message := strings.TrimSpace(FormatError(err))
	if message == "" {
		return err.Error()
	}
	return message
}
