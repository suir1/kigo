package service

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/suir1/kigo/internal/note"
	"github.com/suir1/kigo/internal/secure"
)

func TestPersistentNoteGoClientEditsWithoutPeerAndRecoversLater(t *testing.T) {
	const code = "SOLO-NOTEPAD-2026"
	s := New(Config{NoteStore: t.TempDir(), NoteTTL: time.Hour, SignalRequestsPerMinute: -1})
	server := httptest.NewServer(s.handler())
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	first, err := note.OpenPersistentSession(ctx, note.PersistentOptions{
		ServiceBase: server.URL, Code: code, Pad: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	workspace := note.NewWorkspace()
	if _, err := first.SyncWorkspace(ctx, workspace, "main"); err != nil {
		t.Fatal(err)
	}
	document, err := workspace.Update("main", "written while alone", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Send(ctx, note.FrameFromDocument(note.FrameUpdate, document)); err != nil {
		t.Fatal(err)
	}
	ack, err := first.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ack.Type != note.FrameAck || ack.Revision != document.Revision {
		t.Fatalf("ack = %#v", ack)
	}
	_ = first.Close()

	second, err := note.OpenPersistentSession(ctx, note.PersistentOptions{
		ServiceBase: server.URL, Code: code, Pad: "main",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	later := note.NewWorkspace()
	applied, err := second.SyncWorkspace(ctx, later, "main")
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || later.Snapshot("main").Text != "written while alone" {
		t.Fatalf("recovered = %#v, applied=%#v", later.Snapshot("main"), applied)
	}
}

func TestPersistentNoteBroadcastConflictAndDiskRecovery(t *testing.T) {
	const code = "PROJECT-ALPHA-2026"
	store := t.TempDir()
	token := secure.RoomToken(code)
	padToken, err := note.PersistentPadToken("roadmap")
	if err != nil {
		t.Fatal(err)
	}

	firstServer := New(Config{NoteStore: store, NoteTTL: time.Hour, SignalRequestsPerMinute: -1})
	httpServer := httptest.NewServer(firstServer.handler())
	url := persistentNoteTestURL(httpServer.URL, token, padToken)
	first := dialPersistentNoteTest(t, url)
	second := dialPersistentNoteTest(t, url)
	defer first.Close()
	defer second.Close()
	assertPersistentState(t, first, 0, nil)
	assertPersistentState(t, second, 0, nil)

	document := note.Document{Pad: "roadmap", Text: "available without a peer", Revision: 1, Timestamp: 100}
	record, err := note.SealPersistentDocument(code, document)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.WriteJSON(note.PersistentMessage{
		Type: note.PersistentPut, Version: note.PersistentProtocolVersion, BaseGeneration: 0, Record: &record,
	}); err != nil {
		t.Fatal(err)
	}
	assertPersistentState(t, first, 1, &document)
	assertPersistentState(t, second, 1, &document)
	assertPersistentNoteStoreEncrypted(t, store, code, "roadmap", document.Text)

	staleDocument := note.Document{Pad: "roadmap", Text: "stale", Revision: 2, Timestamp: 200}
	staleRecord, err := note.SealPersistentDocument(code, staleDocument)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.WriteJSON(note.PersistentMessage{
		Type: note.PersistentPut, Version: note.PersistentProtocolVersion, BaseGeneration: 0, Record: &staleRecord,
	}); err != nil {
		t.Fatal(err)
	}
	assertPersistentState(t, second, 1, &document)

	_ = first.Close()
	_ = second.Close()
	httpServer.Close()

	restarted := New(Config{NoteStore: store, NoteTTL: time.Hour, SignalRequestsPerMinute: -1})
	restartedHTTP := httptest.NewServer(restarted.handler())
	defer restartedHTTP.Close()
	recovered := dialPersistentNoteTest(t, persistentNoteTestURL(restartedHTTP.URL, token, padToken))
	defer recovered.Close()
	assertPersistentState(t, recovered, 1, &document)
}

func TestPersistentNoteExpiresFromMemoryAndDisk(t *testing.T) {
	const code = "EXPIRING-NOTEPAD-2026"
	store := t.TempDir()
	token := secure.RoomToken(code)
	padToken, err := note.PersistentPadToken("main")
	if err != nil {
		t.Fatal(err)
	}
	key := token + "\x00" + padToken
	s := New(Config{NoteStore: store, NoteTTL: time.Hour, SignalRequestsPerMinute: -1})
	document := note.Document{Pad: "main", Text: "remove me after expiry", Revision: 1, Timestamp: 100}
	record, err := note.SealPersistentDocument(code, document)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	disk := persistentNoteDiskRecord{
		Version: persistentNoteDiskVersion, Generation: 1,
		UpdatedAt: now.UnixMilli(), ExpiresAt: now.Add(time.Hour).UnixMilli(), Record: record,
	}
	if err := s.writePersistentNote(key, disk); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.loadPersistentNote(key, now)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil {
		t.Fatal("persistent note was not loaded before expiry")
	}
	s.notes[key] = loaded
	s.cleanupPersistentNotes(now.Add(2 * time.Hour))
	if _, ok := s.notes[key]; ok {
		t.Fatal("expired persistent note remains in memory")
	}
	if _, err := os.Stat(s.persistentNotePath(key)); !os.IsNotExist(err) {
		t.Fatalf("expired persistent note file still exists: %v", err)
	}
}

func TestPersistentNoteRejectsInvalidIdentity(t *testing.T) {
	s := New(Config{SignalRequestsPerMinute: -1})
	server := httptest.NewServer(s.handler())
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/note-sync/not-a-token/not-a-pad"
	conn, response, err := websocket.DefaultDialer.Dial(url, nil)
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil || response == nil || response.StatusCode != 400 {
		t.Fatalf("invalid identity response=%v err=%v", response, err)
	}
}

func TestPersistentNoteQuarantinesCorruptSnapshots(t *testing.T) {
	store := t.TempDir()
	s := New(Config{NoteStore: store, NoteTTL: time.Hour, SignalRequestsPerMinute: -1})

	loadPath := s.persistentNotePath("corrupt-on-load")
	loadPayload := []byte(`{"version":`)
	if err := os.WriteFile(loadPath, loadPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.loadPersistentNote("corrupt-on-load", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if loaded != nil {
		t.Fatalf("corrupt snapshot loaded as %#v", loaded)
	}
	assertPersistentNoteQuarantined(t, loadPath, loadPayload)

	cleanupPath := filepath.Join(store, strings.Repeat("a", 64)+".json")
	cleanupPayload := []byte(`{}`)
	if err := os.WriteFile(cleanupPath, cleanupPayload, 0o600); err != nil {
		t.Fatal(err)
	}
	s.cleanupPersistentNoteFiles(time.Now())
	assertPersistentNoteQuarantined(t, cleanupPath, cleanupPayload)
}

func persistentNoteTestURL(base, token, padToken string) string {
	return "ws" + strings.TrimPrefix(base, "http") + "/api/note-sync/" + token + "/" + padToken
}

func dialPersistentNoteTest(t *testing.T, url string) *websocket.Conn {
	t.Helper()
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func assertPersistentState(t *testing.T, conn *websocket.Conn, generation uint64, want *note.Document) {
	t.Helper()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var message note.PersistentMessage
	if err := conn.ReadJSON(&message); err != nil {
		t.Fatal(err)
	}
	if message.Type != note.PersistentState || message.Version != note.PersistentProtocolVersion || message.Generation != generation {
		t.Fatalf("state = %#v", message)
	}
	if want == nil {
		if message.Record != nil {
			t.Fatalf("empty state has record %#v", message.Record)
		}
		return
	}
	if message.Record == nil {
		t.Fatal("persistent state is missing record")
	}
	got, err := note.OpenPersistentDocument("PROJECT-ALPHA-2026", want.Pad, *message.Record)
	if err != nil {
		t.Fatal(err)
	}
	if got != *want {
		t.Fatalf("document = %#v, want %#v", got, *want)
	}
}

func assertPersistentNoteStoreEncrypted(t *testing.T, store string, forbidden ...string) {
	t.Helper()
	entries, err := os.ReadDir(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || filepath.Ext(entries[0].Name()) != ".json" {
		t.Fatalf("persistent note files = %#v", entries)
	}
	payload, err := os.ReadFile(filepath.Join(store, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range forbidden {
		if value != "" && (strings.Contains(entries[0].Name(), value) || strings.Contains(string(payload), value)) {
			t.Fatalf("persistent note storage leaked plaintext %q", value)
		}
	}
}

func assertPersistentNoteQuarantined(t *testing.T, original string, want []byte) {
	t.Helper()
	if _, err := os.Stat(original); !os.IsNotExist(err) {
		t.Fatalf("corrupt snapshot still exists at %s: %v", original, err)
	}
	matches, err := filepath.Glob(original + ".corrupt-*")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("quarantined snapshots = %#v", matches)
	}
	payload, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != string(want) {
		t.Fatalf("quarantined payload = %q, want %q", payload, want)
	}
}
