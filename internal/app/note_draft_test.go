package app

import (
	"path/filepath"
	"testing"
)

func TestNoteDraftPathUsesConfigDirectoryOrOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KIGO_CONFIG_PATH", filepath.Join(dir, "portable", "config.json"))
	t.Setenv("KIGO_NOTE_DRAFT_PATH", "")
	if got, want := noteDraftPath(), filepath.Join(dir, "portable", "note-drafts"); got != want {
		t.Fatalf("note draft path = %q, want %q", got, want)
	}
	override := filepath.Join(dir, "isolated-drafts")
	t.Setenv("KIGO_NOTE_DRAFT_PATH", override)
	if got := noteDraftPath(); got != override {
		t.Fatalf("overridden note draft path = %q", got)
	}
}
