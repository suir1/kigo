package app

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/suir1/kigo/internal/note"
)

func noteDraftPath() string {
	if path := strings.TrimSpace(os.Getenv("KIGO_NOTE_DRAFT_PATH")); path != "" {
		return filepath.Clean(path)
	}
	return filepath.Join(filepath.Dir(userConfigPath()), "note-drafts")
}

func noteDraftRole(host bool) string {
	if host {
		return "host"
	}
	return "join"
}

func loadNoteDraft(store *note.DraftStore, code string, host bool, pad string) (note.Document, bool, error) {
	if store == nil {
		return note.Document{}, false, nil
	}
	return store.Load(code, noteDraftRole(host), pad)
}

func saveNoteDraft(store *note.DraftStore, code string, host bool, document note.Document) error {
	if store == nil {
		return nil
	}
	return store.Save(code, noteDraftRole(host), document)
}
