package note

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRecentStoreTouchesSortsFavoritesAndRemoves(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note-recents.json")
	store := NewRecentStore(path)
	now := time.Unix(1_800_000_000, 0)
	store.now = func() time.Time { return now }
	if err := store.Touch("project-alpha-2026", "roadmap"); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if err := store.Touch("ABC123", "main"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFavorite("PROJECT-ALPHA-2026", "roadmap", true); err != nil {
		t.Fatal(err)
	}
	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].Code != "PROJECT-ALPHA-2026" || !entries[0].Favorite || entries[1].Code != "ABC123" {
		t.Fatalf("entries = %#v", entries)
	}
	if err := store.Remove("PROJECT-ALPHA-2026", "roadmap"); err != nil {
		t.Fatal(err)
	}
	entries, err = store.List()
	if err != nil || len(entries) != 1 || entries[0].Code != "ABC123" {
		t.Fatalf("entries after remove = %#v, err=%v", entries, err)
	}
	if mode := fileMode(t, path).Perm(); mode != 0o600 {
		t.Fatalf("recent store mode = %o", mode)
	}
}

func TestRecentStoreTouchPreservesFavoriteAndPrunes(t *testing.T) {
	store := NewRecentStore(filepath.Join(t.TempDir(), "note-recents.json"))
	now := time.Unix(1_800_000_000, 0)
	store.now = func() time.Time { return now }
	if err := store.Touch("FAVORITE-2026", "main"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetFavorite("FAVORITE-2026", "main", true); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < RecentStoreMaxItems+3; index++ {
		now = now.Add(time.Second)
		code := fmt.Sprintf("RECENT-%02d-2026", index)
		if err := store.Touch(code, "main"); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != RecentStoreMaxItems || entries[0].Code != "FAVORITE-2026" || !entries[0].Favorite {
		t.Fatalf("pruned entries = %#v", entries)
	}
	now = now.Add(time.Minute)
	if err := store.Touch("FAVORITE-2026", "main"); err != nil {
		t.Fatal(err)
	}
	entries, err = store.List()
	if err != nil || !entries[0].Favorite {
		t.Fatalf("retouched favorite = %#v, err=%v", entries, err)
	}
}

func TestRecentStoreRejectsMissingFavoriteAndDoesNotStoreText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "note-recents.json")
	store := NewRecentStore(path)
	if err := store.SetFavorite("ABC123", "main", true); !errors.Is(err, ErrRecentNotFound) {
		t.Fatalf("missing favorite err = %v", err)
	}
	if err := store.Touch("PROJECT-ALPHA-2026", "Sprint Notes"); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(payload, []byte("document body")) {
		t.Fatal("recent catalog unexpectedly stores document text")
	}
}
