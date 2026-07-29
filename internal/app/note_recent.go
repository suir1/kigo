package app

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/suir1/kigo/internal/note"
	"github.com/suir1/kigo/internal/secure"
)

func noteRecentsPath() string {
	if path := strings.TrimSpace(os.Getenv("KIGO_NOTE_RECENTS_PATH")); path != "" {
		return filepath.Clean(path)
	}
	return filepath.Join(filepath.Dir(userConfigPath()), "note-recents.json")
}

func noteRecentStore() *note.RecentStore {
	return note.NewRecentStore(noteRecentsPath())
}

func listRecentNotes(out io.Writer, asJSON bool) error {
	entries, err := noteRecentStore().List()
	if err != nil {
		return err
	}
	if asJSON {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(entries)
	}
	if len(entries) == 0 {
		_, err = fmt.Fprintln(out, "No recent notepads.")
		return err
	}
	for _, entry := range entries {
		marker := " "
		if entry.Favorite {
			marker = "*"
		}
		if _, err := fmt.Fprintf(out, "%s %s\t%s\t%s\n", marker, entry.Code, entry.Pad, time.UnixMilli(entry.LastOpened).Format(time.RFC3339)); err != nil {
			return err
		}
	}
	return nil
}

func setRecentNoteFavorite(code, pad string, favorite bool) error {
	code, err := secure.ValidateCode(code)
	if err != nil {
		return err
	}
	return noteRecentStore().SetFavorite(code, note.NormalizePad(pad), favorite)
}

func forgetRecentNote(code, pad string) error {
	code, err := secure.ValidateCode(code)
	if err != nil {
		return err
	}
	return noteRecentStore().Remove(code, note.NormalizePad(pad))
}
