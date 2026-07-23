package transfer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/suir1/kigo/internal/protocol"
)

func TestPreparePathUsesGitIgnoreStack(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	if err := os.MkdirAll(filepath.Join(root, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(parent, ".gitignore"), []byte("*.tmp\n!keep.tmp\nbuild/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string]string{
		"visible.txt":        "visible",
		"drop.tmp":           "drop",
		"keep.tmp":           "keep",
		"build/artifact.bin": "artifact",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	prepared, err := PreparePath(root)
	if err != nil {
		t.Fatal(err)
	}
	got := itemNames(prepared.items)
	for _, want := range []string{"project", "project/visible.txt", "project/keep.tmp"} {
		if !got[want] {
			t.Fatalf("prepared items missing %q: %#v", want, got)
		}
	}
	for _, ignored := range []string{"project/drop.tmp", "project/build", "project/build/artifact.bin"} {
		if got[ignored] {
			t.Fatalf("ignored path was prepared: %s", ignored)
		}
	}
	summary := prepared.Summary()
	if summary.Files != 2 || summary.Directories != 1 || summary.Ignored != 2 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestPreparePathCanDisableGitIgnore(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.tmp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "included.tmp"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err := PreparePathWithOptions(root, PrepareOptions{
		Symlinks:    SymlinkFollow,
		NoGitIgnore: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := itemNames(prepared.items)
	name := filepath.Base(root)
	if !got[name+"/included.tmp"] {
		t.Fatalf("disabled gitignore did not include file: %#v", got)
	}
	if prepared.Summary().Ignored != 0 {
		t.Fatalf("ignored count = %d", prepared.Summary().Ignored)
	}
}

func TestPreparedSummaryString(t *testing.T) {
	summary := PreparedSummary{
		Files:       1,
		Directories: 2,
		Symlinks:    1,
		Bytes:       1536,
		Ignored:     3,
	}
	want := "1 file, 2 directories, 1 symlink, 1.50 KB, 3 ignored"
	if got := summary.String(); got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}
}

func itemNames(items []protocol.Item) map[string]bool {
	names := make(map[string]bool, len(items))
	for _, item := range items {
		names[item.Name] = true
	}
	return names
}
