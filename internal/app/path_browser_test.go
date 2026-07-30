package app

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestPathBrowserListsSortsAndFiltersEntries(t *testing.T) {
	root := t.TempDir()
	olderDir := filepath.Join(root, "older")
	newerDir := filepath.Join(root, "newer")
	if err := os.Mkdir(olderDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(newerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	olderFile := filepath.Join(root, "a.txt")
	newerFile := filepath.Join(root, "z.txt")
	if err := os.WriteFile(olderFile, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newerFile, []byte("z"), 0o600); err != nil {
		t.Fatal(err)
	}
	older := time.Now().Add(-time.Hour)
	newer := time.Now()
	for _, path := range []string{olderDir, olderFile} {
		if err := os.Chtimes(path, older, older); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{newerDir, newerFile} {
		if err := os.Chtimes(path, newer, newer); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := listPathBrowserEntries(root, pathPickFileOrDirectory, pathBrowserSortName)
	if err != nil {
		t.Fatal(err)
	}
	labels := pathBrowserLabels(entries)
	parentLabel := pathBrowserDirectoryLabel("..")
	newerLabel := pathBrowserDirectoryLabel("newer")
	olderLabel := pathBrowserDirectoryLabel("older")
	for _, expected := range []string{parentLabel, newerLabel, olderLabel, "a.txt", "z.txt", "[Select this folder]"} {
		if !slices.Contains(labels, expected) {
			t.Fatalf("labels %#v missing %q", labels, expected)
		}
	}
	if slices.Index(labels, newerLabel) > slices.Index(labels, "a.txt") {
		t.Fatalf("directories should be listed before files: %#v", labels)
	}

	modified, err := listPathBrowserEntries(root, pathPickFileOrDirectory, pathBrowserSortModified)
	if err != nil {
		t.Fatal(err)
	}
	modifiedLabels := pathBrowserLabels(modified)
	if slices.Index(modifiedLabels, newerLabel) > slices.Index(modifiedLabels, olderLabel) ||
		slices.Index(modifiedLabels, "z.txt") > slices.Index(modifiedLabels, "a.txt") {
		t.Fatalf("modified order = %#v", modifiedLabels)
	}

	filtered := filterPathBrowserEntries(entries, "z.")
	filteredLabels := pathBrowserLabels(filtered)
	if !slices.Contains(filteredLabels, parentLabel) ||
		!slices.Contains(filteredLabels, "z.txt") ||
		!slices.Contains(filteredLabels, "[Select this folder]") ||
		slices.Contains(filteredLabels, "a.txt") {
		t.Fatalf("filtered labels = %#v", filteredLabels)
	}
}

func TestPathBrowserDirectoryOnlyAndNormalization(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "child")
	file := filepath.Join(root, "file.txt")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := listPathBrowserEntries(root, pathPickDirectoryOnly, pathBrowserSortName)
	if err != nil {
		t.Fatal(err)
	}
	labels := pathBrowserLabels(entries)
	if !slices.Contains(labels, pathBrowserDirectoryLabel("child")) || slices.Contains(labels, "file.txt") {
		t.Fatalf("directory-only labels = %#v", labels)
	}
	normalized, err := normalizeBrowserDirectory(file)
	if err != nil {
		t.Fatal(err)
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	if normalized != absoluteRoot {
		t.Fatalf("normalized = %q, want %q", normalized, absoluteRoot)
	}
	normalized, err = normalizeBrowserDirectory(filepath.Join(root, "missing", "file.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if normalized != absoluteRoot {
		t.Fatalf("missing path normalized = %q, want %q", normalized, absoluteRoot)
	}
}

func pathBrowserLabels(entries []pathBrowserEntry) []string {
	labels := make([]string, len(entries))
	for index, entry := range entries {
		labels[index] = entry.Label
	}
	return labels
}

func pathBrowserDirectoryLabel(name string) string {
	return name + string(os.PathSeparator)
}
