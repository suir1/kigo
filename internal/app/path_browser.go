package app

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type pathPickMode int

const (
	pathPickFileOrDirectory pathPickMode = iota
	pathPickDirectoryOnly
)

type pathBrowserSort int

const (
	pathBrowserSortName pathBrowserSort = iota
	pathBrowserSortModified
)

type pathBrowserEntry struct {
	Label         string
	Path          string
	IsDir         bool
	Parent        bool
	SelectCurrent bool
	Modified      time.Time
}

func normalizeBrowserDirectory(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "."
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(absolute)
	if err == nil {
		if info.IsDir() {
			return filepath.Clean(absolute), nil
		}
		return filepath.Dir(absolute), nil
	}

	current := filepath.Dir(absolute)
	for {
		info, statErr := os.Stat(current)
		if statErr == nil && info.IsDir() {
			return filepath.Clean(current), nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no accessible parent directory for %s", path)
		}
		current = parent
	}
}

func listPathBrowserEntries(dir string, mode pathPickMode, sortMode pathBrowserSort) ([]pathBrowserEntry, error) {
	dir, err := normalizeBrowserDirectory(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", dir)
	}

	raw, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	directories := make([]pathBrowserEntry, 0, len(raw))
	files := make([]pathBrowserEntry, 0, len(raw))
	for _, item := range raw {
		path := filepath.Join(dir, item.Name())
		info, infoErr := item.Info()
		if infoErr != nil {
			continue
		}
		entry := pathBrowserEntry{
			Label:    item.Name(),
			Path:     path,
			IsDir:    item.IsDir(),
			Modified: info.ModTime(),
		}
		if entry.IsDir {
			entry.Label += string(os.PathSeparator)
			directories = append(directories, entry)
		} else if mode == pathPickFileOrDirectory {
			files = append(files, entry)
		}
	}
	sortPathBrowserEntries(directories, sortMode)
	sortPathBrowserEntries(files, sortMode)

	entries := make([]pathBrowserEntry, 0, len(directories)+len(files)+2)
	parent := filepath.Dir(dir)
	if parent != dir {
		entries = append(entries, pathBrowserEntry{
			Label:  ".." + string(os.PathSeparator),
			Path:   parent,
			IsDir:  true,
			Parent: true,
		})
	}
	entries = append(entries, directories...)
	entries = append(entries, files...)
	entries = append(entries, pathBrowserEntry{
		Label:         "[Select this folder]",
		Path:          dir,
		IsDir:         true,
		SelectCurrent: true,
	})
	return entries, nil
}

func sortPathBrowserEntries(entries []pathBrowserEntry, sortMode pathBrowserSort) {
	sort.Slice(entries, func(i, j int) bool {
		if sortMode == pathBrowserSortModified && !entries[i].Modified.Equal(entries[j].Modified) {
			return entries[i].Modified.After(entries[j].Modified)
		}
		return strings.ToLower(entries[i].Label) < strings.ToLower(entries[j].Label)
	})
}

func filterPathBrowserEntries(entries []pathBrowserEntry, filter string) []pathBrowserEntry {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return append([]pathBrowserEntry(nil), entries...)
	}
	filtered := make([]pathBrowserEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.Parent || entry.SelectCurrent || strings.Contains(strings.ToLower(entry.Label), filter) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}
