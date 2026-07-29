package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/suir1/kigo/internal/note"
	"github.com/suir1/kigo/internal/transport"
)

func TestLocalWebNoteStoreHostUpdateRemoteClearAndLeave(t *testing.T) {
	peer := newFakeLocalWebNotePeer()
	store := newLocalWebNoteStoreWithConnector(
		context.Background(),
		&globalOptions{WebURL: "https://kigo.example"},
		func(context.Context, string, bool, string) (localWebNotePeer, error) {
			return peer, nil
		},
	)
	store.generateCode = func() (string, error) { return "K7M9Q2", nil }

	started, err := store.StartHost()
	if err != nil {
		t.Fatal(err)
	}
	if !started.Running || !started.Host || started.Code != "K7M9Q2" ||
		started.Link != "https://kigo.example/#n=K7M9Q2" ||
		started.Status != "opening" {
		t.Fatalf("started state = %#v", started)
	}
	waitForLocalWebNote(t, store, func(state localWebNoteSnapshot) bool {
		return state.Connected && state.Status == "available"
	})

	state, err := store.Update("local document")
	if err != nil {
		t.Fatal(err)
	}
	if state.Revision != 1 || state.Synced || state.Status != "syncing" {
		t.Fatalf("update state = %#v", state)
	}
	update := receiveFakeLocalWebNoteFrame(t, peer.sent)
	if update.Type != note.FrameUpdate || update.Text != "local document" || update.Revision != 1 {
		t.Fatalf("update frame = %#v", update)
	}

	peer.recv <- note.Frame{
		Type:     note.FrameAck,
		Version:  note.ProtocolVersion,
		Pad:      note.DefaultPad,
		Revision: 1,
	}
	state = waitForLocalWebNote(t, store, func(state localWebNoteSnapshot) bool {
		return state.Synced && state.AckedRevision == 1
	})
	if state.Status != "synced" {
		t.Fatalf("acked state = %#v", state)
	}

	peer.recv <- note.FrameFromDocument(note.FrameUpdate, note.Document{
		Pad:       note.DefaultPad,
		Text:      "remote document",
		Revision:  2,
		Timestamp: time.Now().UnixMilli() + 100,
	})
	state = waitForLocalWebNote(t, store, func(state localWebNoteSnapshot) bool {
		return state.Text == "remote document" && state.Revision == 2
	})
	if !state.Synced {
		t.Fatalf("remote state = %#v", state)
	}
	ack := receiveFakeLocalWebNoteFrame(t, peer.sent)
	if ack.Type != note.FrameAck || ack.Revision != 2 {
		t.Fatalf("remote ack = %#v", ack)
	}

	state, err = store.Clear()
	if err != nil {
		t.Fatal(err)
	}
	if state.Text != "" || state.Revision != 3 || state.Synced {
		t.Fatalf("clear state = %#v", state)
	}
	clearFrame := receiveFakeLocalWebNoteFrame(t, peer.sent)
	if clearFrame.Type != note.FrameClear || clearFrame.Revision != 3 || clearFrame.Text != "" {
		t.Fatalf("clear frame = %#v", clearFrame)
	}

	left := store.Leave()
	if left.Running || left.Connected || left.Status != "left" {
		t.Fatalf("left state = %#v", left)
	}
	bye := receiveFakeLocalWebNoteFrame(t, peer.sent)
	if bye.Type != note.FrameBye {
		t.Fatalf("leave frame = %#v", bye)
	}
}

func TestLocalWebNoteStoreHostsCustomCode(t *testing.T) {
	peer := newFakeLocalWebNotePeer()
	store := newLocalWebNoteStoreWithConnector(
		context.Background(),
		&globalOptions{WebURL: "https://kigo.example"},
		func(context.Context, string, bool, string) (localWebNotePeer, error) { return peer, nil },
	)
	started, err := store.StartHostWithCodeAndPad(" project-Alpha-2026 ", "release notes")
	if err != nil {
		t.Fatal(err)
	}
	if started.Code != "PROJECT-ALPHA-2026" || started.Pad != "release notes" ||
		started.Link != "https://kigo.example/#n=PROJECT-ALPHA-2026&p=release+notes" {
		t.Fatalf("started = %#v", started)
	}
	waitForLocalWebNote(t, store, func(state localWebNoteSnapshot) bool { return state.Connected })
	if _, err := store.Update("custom pad text"); err != nil {
		t.Fatal(err)
	}
	frame := receiveFakeLocalWebNoteFrame(t, peer.sent)
	if frame.Pad != "release notes" {
		t.Fatalf("custom pad frame = %#v", frame)
	}
	store.Leave()
}

func TestLocalWebNoteStoreAppliesPeerLeave(t *testing.T) {
	peer := newFakeLocalWebNotePeer()
	store := newLocalWebNoteStoreWithConnector(
		context.Background(),
		&globalOptions{},
		func(context.Context, string, bool, string) (localWebNotePeer, error) {
			return peer, nil
		},
	)
	if _, err := store.StartJoin("K7M9Q2"); err != nil {
		t.Fatal(err)
	}
	waitForLocalWebNote(t, store, func(state localWebNoteSnapshot) bool {
		return state.Connected
	})
	peer.recv <- note.Frame{
		Type:    note.FrameBye,
		Version: note.ProtocolVersion,
		Pad:     note.DefaultPad,
	}
	state := waitForLocalWebNote(t, store, func(state localWebNoteSnapshot) bool {
		return !state.Running
	})
	if state.Status != "peer_left" || state.Error != "" {
		t.Fatalf("peer-left state = %#v", state)
	}
}

func TestLocalWebNoteStoreReconnectsAndRetainsWorkspace(t *testing.T) {
	first := newFakeLocalWebNotePeer()
	second := newFakeLocalWebNotePeer()
	peers := []localWebNotePeer{first, second}
	var connectMu sync.Mutex
	connectCount := 0
	store := newLocalWebNoteStoreWithConnector(
		context.Background(),
		&globalOptions{ReconnectAttempts: 2, ReconnectDelay: 20 * time.Millisecond},
		func(context.Context, string, bool, string) (localWebNotePeer, error) {
			connectMu.Lock()
			defer connectMu.Unlock()
			if connectCount >= len(peers) {
				return nil, transport.ErrClosed
			}
			peer := peers[connectCount]
			connectCount++
			return peer, nil
		},
	)

	if _, err := store.StartHostWithCodeAndPad("PROJECT-ALPHA-2026", "Sprint Notes"); err != nil {
		t.Fatal(err)
	}
	waitForLocalWebNote(t, store, func(state localWebNoteSnapshot) bool { return state.Connected })
	if _, err := store.Update("draft before disconnect"); err != nil {
		t.Fatal(err)
	}
	update := receiveFakeLocalWebNoteFrame(t, first.sent)
	if update.Revision != 1 || update.Text != "draft before disconnect" {
		t.Fatalf("first update = %#v", update)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	waitForLocalWebNote(t, store, func(state localWebNoteSnapshot) bool {
		return state.Running && !state.Connected && state.Status == "reconnecting"
	})
	state := waitForLocalWebNote(t, store, func(state localWebNoteSnapshot) bool {
		return state.Connected && state.Status == "available"
	})
	if state.Text != "draft before disconnect" || state.Revision != 1 || !state.Synced {
		t.Fatalf("reconnected state = %#v", state)
	}

	if _, err := store.Update("draft after reconnect"); err != nil {
		t.Fatal(err)
	}
	update = receiveFakeLocalWebNoteFrame(t, second.sent)
	if update.Revision != 2 || update.Text != "draft after reconnect" || update.Pad != "Sprint Notes" {
		t.Fatalf("second update = %#v", update)
	}
	store.Leave()
}

func TestLocalWebNoteStoreRestoresEncryptedDraft(t *testing.T) {
	draftPath := filepath.Join(t.TempDir(), "note-drafts")
	firstPeer := newFakeLocalWebNotePeer()
	first := newLocalWebNoteStoreWithConnector(
		context.Background(),
		&globalOptions{},
		func(context.Context, string, bool, string) (localWebNotePeer, error) { return firstPeer, nil },
	)
	first.drafts = note.NewDraftStore(draftPath)
	if _, err := first.StartHostWithCodeAndPad("PROJECT-ALPHA-2026", "Sprint Notes"); err != nil {
		t.Fatal(err)
	}
	waitForLocalWebNote(t, first, func(state localWebNoteSnapshot) bool { return state.Connected })
	if _, err := first.Update("persisted local web draft"); err != nil {
		t.Fatal(err)
	}
	receiveFakeLocalWebNoteFrame(t, firstPeer.sent)
	first.Leave()
	receiveFakeLocalWebNoteFrame(t, firstPeer.sent)

	secondPeer := newFakeLocalWebNotePeer()
	second := newLocalWebNoteStoreWithConnector(
		context.Background(),
		&globalOptions{},
		func(context.Context, string, bool, string) (localWebNotePeer, error) { return secondPeer, nil },
	)
	second.drafts = note.NewDraftStore(draftPath)
	started, err := second.StartHostWithCodeAndPad("PROJECT-ALPHA-2026", "Sprint Notes")
	if err != nil {
		t.Fatal(err)
	}
	if !started.DraftRecovered || started.Text != "persisted local web draft" || started.Revision != 1 {
		t.Fatalf("restored state = %#v", started)
	}
	second.Leave()
}

func TestLocalWebNoteAPIAuthValidationAndHostLink(t *testing.T) {
	peer := newFakeLocalWebNotePeer()
	store := newLocalWebNoteStoreWithConnector(
		context.Background(),
		&globalOptions{WebURL: "https://kigo.example"},
		func(context.Context, string, bool, string) (localWebNotePeer, error) {
			return peer, nil
		},
	)
	store.generateCode = func() (string, error) { return "K7M9Q2", nil }
	server := &localWebServer{
		token: "test-token",
		job: &nativeTaskStore{
			run: func(context.Context, nativeTaskRequest, func(nativeTaskEvent)) error { return nil },
		},
		note: store,
	}
	handler := server.handler()

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/note", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	host := localWebJSONRequest(t, handler, http.MethodPost, "/api/note/host", `{"code":"project-alpha-2026","pad":"roadmap"}`)
	if host.Code != http.StatusAccepted {
		t.Fatalf("host status = %d body=%s", host.Code, host.Body.String())
	}
	if !strings.Contains(host.Body.String(), `"code":"PROJECT-ALPHA-2026"`) ||
		!strings.Contains(host.Body.String(), `"pad":"roadmap"`) ||
		!strings.Contains(host.Body.String(), `"link":"https://kigo.example/#n=PROJECT-ALPHA-2026\u0026p=roadmap"`) {
		t.Fatalf("host body = %s", host.Body.String())
	}
	store.Leave()

	invalid := localWebJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/api/note/join",
		`{"code":"bad"}`,
	)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid join status = %d body=%s", invalid.Code, invalid.Body.String())
	}
	invalidPad := localWebJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/api/note/join",
		`{"code":"K7M9Q2","pad":"bad\npad"}`,
	)
	if invalidPad.Code != http.StatusBadRequest || !strings.Contains(invalidPad.Body.String(), "note pad") {
		t.Fatalf("invalid pad status = %d body=%s", invalidPad.Code, invalidPad.Body.String())
	}

	tooLarge := localWebJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/api/note/update",
		`{"text":"`+strings.Repeat("x", note.MaxTextSize+1)+`"}`,
	)
	if tooLarge.Code != http.StatusBadRequest {
		t.Fatalf("oversize update status = %d body=%s", tooLarge.Code, tooLarge.Body.String())
	}
}

func TestLocalWebNoteRecentAPIReopensFavoritesAndForgets(t *testing.T) {
	peer := newFakeLocalWebNotePeer()
	store := newLocalWebNoteStoreWithConnector(
		context.Background(),
		&globalOptions{},
		func(context.Context, string, bool, string) (localWebNotePeer, error) {
			return peer, nil
		},
	)
	store.recents = note.NewRecentStore(filepath.Join(t.TempDir(), "note-recents.json"))
	server := &localWebServer{token: "test-token", note: store}
	handler := server.handler()

	if _, err := store.StartJoinPad("PROJECT-ALPHA-2026", "roadmap"); err != nil {
		t.Fatal(err)
	}
	waitForLocalWebNote(t, store, func(state localWebNoteSnapshot) bool { return state.Connected })

	recents := localWebJSONRequest(t, handler, http.MethodGet, "/api/note/recents", "")
	if recents.Code != http.StatusOK ||
		!strings.Contains(recents.Body.String(), `"code":"PROJECT-ALPHA-2026"`) ||
		!strings.Contains(recents.Body.String(), `"pad":"roadmap"`) {
		t.Fatalf("recent list status=%d body=%s", recents.Code, recents.Body.String())
	}

	favorite := localWebJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/api/note/recents/favorite",
		`{"code":"PROJECT-ALPHA-2026","pad":"roadmap","favorite":true}`,
	)
	if favorite.Code != http.StatusOK {
		t.Fatalf("favorite status=%d body=%s", favorite.Code, favorite.Body.String())
	}
	recents = localWebJSONRequest(t, handler, http.MethodGet, "/api/note/recents", "")
	if !strings.Contains(recents.Body.String(), `"favorite":true`) {
		t.Fatalf("favorite list body=%s", recents.Body.String())
	}

	forget := localWebJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/api/note/recents/forget",
		`{"code":"PROJECT-ALPHA-2026","pad":"roadmap"}`,
	)
	if forget.Code != http.StatusOK {
		t.Fatalf("forget status=%d body=%s", forget.Code, forget.Body.String())
	}
	recents = localWebJSONRequest(t, handler, http.MethodGet, "/api/note/recents", "")
	if strings.TrimSpace(recents.Body.String()) != "[]" {
		t.Fatalf("forgotten recent list body=%s", recents.Body.String())
	}
	store.Leave()
}

func TestLocalWebNoteFailedConnectionDoesNotCreateRecent(t *testing.T) {
	store := newLocalWebNoteStoreWithConnector(
		context.Background(),
		&globalOptions{},
		func(context.Context, string, bool, string) (localWebNotePeer, error) {
			return nil, transport.ErrClosed
		},
	)
	store.recents = note.NewRecentStore(filepath.Join(t.TempDir(), "note-recents.json"))
	if _, err := store.StartJoin("K7M9Q2"); err != nil {
		t.Fatal(err)
	}
	waitForLocalWebNote(t, store, func(state localWebNoteSnapshot) bool {
		return !state.Running && state.Status == "error"
	})
	entries, err := store.RecentNotes()
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed connection recents=%#v err=%v", entries, err)
	}
}

func TestLocalWebRejectsNativeTaskAndNotepadOverlap(t *testing.T) {
	taskStarted := make(chan struct{})
	taskRelease := make(chan struct{})
	job := &nativeTaskStore{
		run: func(ctx context.Context, _ nativeTaskRequest, _ func(nativeTaskEvent)) error {
			close(taskStarted)
			select {
			case <-taskRelease:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
	}
	peer := newFakeLocalWebNotePeer()
	store := newLocalWebNoteStoreWithConnector(
		context.Background(),
		&globalOptions{},
		func(context.Context, string, bool, string) (localWebNotePeer, error) {
			return peer, nil
		},
	)
	store.generateCode = func() (string, error) { return "K7M9Q2", nil }
	server := &localWebServer{
		token: "test-token",
		job:   job,
		note:  store,
	}
	handler := server.handler()

	doctor := localWebJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/api/doctor",
		`{"timeout":"2s"}`,
	)
	if doctor.Code != http.StatusAccepted {
		t.Fatalf("doctor status = %d body=%s", doctor.Code, doctor.Body.String())
	}
	<-taskStarted
	hostWhileTask := localWebJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/api/note/host",
		`{}`,
	)
	if hostWhileTask.Code != http.StatusConflict {
		t.Fatalf("host during task status = %d body=%s", hostWhileTask.Code, hostWhileTask.Body.String())
	}

	close(taskRelease)
	waitForLocalWebJob(t, job, func(state nativeTaskSnapshot) bool { return !state.Running })
	host := localWebJSONRequest(t, handler, http.MethodPost, "/api/note/host", `{}`)
	if host.Code != http.StatusAccepted {
		t.Fatalf("host status = %d body=%s", host.Code, host.Body.String())
	}
	taskWhileHost := localWebJSONRequest(
		t,
		handler,
		http.MethodPost,
		"/api/doctor",
		`{"timeout":"2s"}`,
	)
	if taskWhileHost.Code != http.StatusConflict {
		t.Fatalf("task during host status = %d body=%s", taskWhileHost.Code, taskWhileHost.Body.String())
	}
	store.Leave()
}

type fakeLocalWebNotePeer struct {
	recv   chan note.Frame
	sent   chan note.Frame
	closed chan struct{}
	once   sync.Once
}

func newFakeLocalWebNotePeer() *fakeLocalWebNotePeer {
	return &fakeLocalWebNotePeer{
		recv:   make(chan note.Frame, 16),
		sent:   make(chan note.Frame, 16),
		closed: make(chan struct{}),
	}
}

func (p *fakeLocalWebNotePeer) Send(ctx context.Context, frame note.Frame) error {
	select {
	case p.sent <- frame:
		return nil
	case <-p.closed:
		return transport.ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *fakeLocalWebNotePeer) Recv(ctx context.Context) (note.Frame, error) {
	select {
	case frame := <-p.recv:
		return frame, nil
	case <-p.closed:
		return note.Frame{}, transport.ErrClosed
	case <-ctx.Done():
		return note.Frame{}, ctx.Err()
	}
}

func (p *fakeLocalWebNotePeer) Close() error {
	p.once.Do(func() {
		close(p.closed)
	})
	return nil
}

func waitForLocalWebNote(
	t *testing.T,
	store *localWebNoteStore,
	condition func(localWebNoteSnapshot) bool,
) localWebNoteSnapshot {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		state := store.Snapshot()
		if condition(state) {
			return state
		}
		time.Sleep(time.Millisecond)
	}
	state := store.Snapshot()
	t.Fatalf("note condition not met: %#v", state)
	return localWebNoteSnapshot{}
}

func receiveFakeLocalWebNoteFrame(t *testing.T, frames <-chan note.Frame) note.Frame {
	t.Helper()
	select {
	case frame := <-frames:
		return frame
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for note frame")
		return note.Frame{}
	}
}
