package note

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDraftStoreEncryptsRoundTripsAndSeparatesRoles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note-drafts")
	store := NewDraftStore(path)
	now := time.Unix(1_800_000_000, 0)
	store.now = func() time.Time { return now }
	document := Document{
		Pad:       "Sprint Notes",
		Text:      "private draft text",
		Revision:  7,
		Timestamp: now.UnixMilli(),
	}
	if err := store.Save("PROJECT-ALPHA-2026", "host", document); err != nil {
		t.Fatal(err)
	}
	data, entryPath := readOnlyDraftEntry(t, path)
	for _, secret := range [][]byte{[]byte(document.Text), []byte(document.Pad), []byte("PROJECT-ALPHA-2026")} {
		if bytes.Contains(data, secret) {
			t.Fatalf("draft file exposed %q: %s", secret, data)
		}
	}
	loaded, ok, err := store.Load("project-alpha-2026", "host", "Sprint Notes")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || loaded != document {
		t.Fatalf("loaded = %#v, %v", loaded, ok)
	}
	if _, ok, err := store.Load("PROJECT-ALPHA-2026", "join", "Sprint Notes"); err != nil || ok {
		t.Fatalf("join role loaded host draft: ok=%v err=%v", ok, err)
	}
	if mode := fileMode(t, entryPath); mode.Perm() != 0o600 {
		t.Fatalf("draft mode = %o", mode.Perm())
	}
}

func TestDraftStoreExpiresAndOverwritesDocuments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note-drafts")
	store := NewDraftStore(path)
	now := time.Unix(1_800_000_000, 0)
	store.now = func() time.Time { return now }
	first := Document{Pad: DefaultPad, Text: "first", Revision: 1, Timestamp: now.UnixMilli()}
	if err := store.Save("ABC123", "join", first); err != nil {
		t.Fatal(err)
	}
	second := Document{Pad: DefaultPad, Text: "second", Revision: 2, Timestamp: now.Add(time.Second).UnixMilli()}
	if err := store.Save("ABC123", "join", second); err != nil {
		t.Fatal(err)
	}
	loaded, ok, err := store.Load("ABC123", "join", DefaultPad)
	if err != nil || !ok || loaded != second {
		t.Fatalf("overwritten draft = %#v, %v, %v", loaded, ok, err)
	}
	store.now = func() time.Time { return now.Add(DraftTTL + time.Second) }
	if _, ok, err := store.Load("ABC123", "join", DefaultPad); err != nil || ok {
		t.Fatalf("expired draft loaded: ok=%v err=%v", ok, err)
	}
}

func fileMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode()
}

func readOnlyDraftEntry(t *testing.T, path string) ([]byte, string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("draft entries = %d", len(entries))
	}
	entryPath := filepath.Join(path, entries[0].Name())
	data, err := os.ReadFile(entryPath)
	if err != nil {
		t.Fatal(err)
	}
	return data, entryPath
}
