package app

import (
	"os"
	"path/filepath"
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
	for _, expected := range []string{"../", "newer/", "older/", "a.txt", "z.txt", "[Select this folder]"} {
		if !containsString(labels, expected) {
			t.Fatalf("labels %#v missing %q", labels, expected)
		}
	}
	if indexOfString(labels, "newer/") > indexOfString(labels, "a.txt") {
		t.Fatalf("directories should be listed before files: %#v", labels)
	}

	modified, err := listPathBrowserEntries(root, pathPickFileOrDirectory, pathBrowserSortModified)
	if err != nil {
		t.Fatal(err)
	}
	modifiedLabels := pathBrowserLabels(modified)
	if indexOfString(modifiedLabels, "newer/") > indexOfString(modifiedLabels, "older/") ||
		indexOfString(modifiedLabels, "z.txt") > indexOfString(modifiedLabels, "a.txt") {
		t.Fatalf("modified order = %#v", modifiedLabels)
	}

	filtered := filterPathBrowserEntries(entries, "z.")
	filteredLabels := pathBrowserLabels(filtered)
	if !containsString(filteredLabels, "../") ||
		!containsString(filteredLabels, "z.txt") ||
		!containsString(filteredLabels, "[Select this folder]") ||
		containsString(filteredLabels, "a.txt") {
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
	if !containsString(labels, "child/") || containsString(labels, "file.txt") {
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

func containsString(values []string, target string) bool {
	return indexOfString(values, target) >= 0
}

func indexOfString(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
}
