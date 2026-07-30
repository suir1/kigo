package note

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/suir1/kigo/internal/localstate"
	"github.com/suir1/kigo/internal/secure"
)

const (
	recentStoreVersion  = 1
	recentStoreMaxBytes = 64 << 10
	RecentStoreMaxItems = 20
)

var ErrRecentNotFound = errors.New("recent notepad not found")

type RecentEntry struct {
	Code       string `json:"code"`
	Pad        string `json:"pad"`
	Favorite   bool   `json:"favorite,omitempty"`
	LastOpened int64  `json:"last_opened"`
}

type recentFile struct {
	Version int           `json:"version"`
	Entries []RecentEntry `json:"entries"`
}

type RecentStore struct {
	path string
	now  func() time.Time
}

var recentFileMu sync.Mutex

func NewRecentStore(path string) *RecentStore {
	return &RecentStore{path: filepath.Clean(path), now: time.Now}
}

func (s *RecentStore) List() ([]RecentEntry, error) {
	if !s.available() {
		return []RecentEntry{}, nil
	}
	recentFileMu.Lock()
	defer recentFileMu.Unlock()
	entries, err := readRecentEntries(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []RecentEntry{}, nil
	}
	if err != nil {
		return nil, err
	}
	sortRecentEntries(entries)
	return append([]RecentEntry{}, entries...), nil
}

func (s *RecentStore) Touch(code, pad string) error {
	code, pad, err := validateRecentIdentity(code, pad)
	if err != nil {
		return err
	}
	if !s.available() {
		return nil
	}
	return s.update(func(entries []RecentEntry) ([]RecentEntry, error) {
		favorite := false
		kept := entries[:0]
		for _, entry := range entries {
			if entry.Code == code && entry.Pad == pad {
				favorite = entry.Favorite
				continue
			}
			kept = append(kept, entry)
		}
		entries = append(kept, RecentEntry{
			Code: code, Pad: pad, Favorite: favorite, LastOpened: s.clock().UnixMilli(),
		})
		sortRecentEntries(entries)
		if len(entries) > RecentStoreMaxItems {
			entries = entries[:RecentStoreMaxItems]
		}
		return entries, nil
	})
}

func (s *RecentStore) SetFavorite(code, pad string, favorite bool) error {
	code, pad, err := validateRecentIdentity(code, pad)
	if err != nil {
		return err
	}
	if !s.available() {
		return ErrRecentNotFound
	}
	return s.update(func(entries []RecentEntry) ([]RecentEntry, error) {
		found := false
		for index := range entries {
			if entries[index].Code == code && entries[index].Pad == pad {
				entries[index].Favorite = favorite
				found = true
				break
			}
		}
		if !found {
			return nil, ErrRecentNotFound
		}
		sortRecentEntries(entries)
		return entries, nil
	})
}

func (s *RecentStore) Remove(code, pad string) error {
	code, pad, err := validateRecentIdentity(code, pad)
	if err != nil {
		return err
	}
	if !s.available() {
		return nil
	}
	return s.update(func(entries []RecentEntry) ([]RecentEntry, error) {
		kept := entries[:0]
		for _, entry := range entries {
			if entry.Code != code || entry.Pad != pad {
				kept = append(kept, entry)
			}
		}
		return kept, nil
	})
}

func (s *RecentStore) update(change func([]RecentEntry) ([]RecentEntry, error)) error {
	recentFileMu.Lock()
	defer recentFileMu.Unlock()
	return localstate.WithFileLock(s.path, func() error {
		entries, err := readRecentEntries(s.path)
		if errors.Is(err, os.ErrNotExist) {
			entries = []RecentEntry{}
		} else if err != nil {
			return err
		}
		entries, err = change(entries)
		if err != nil {
			return err
		}
		return localstate.WriteJSON(s.path, recentFile{
			Version: recentStoreVersion,
			Entries: entries,
		})
	})
}

func (s *RecentStore) available() bool {
	return s != nil && strings.TrimSpace(s.path) != "" && s.path != "."
}

func (s *RecentStore) clock() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

func validateRecentIdentity(code, pad string) (string, string, error) {
	code, err := secure.ValidateCode(code)
	if err != nil {
		return "", "", err
	}
	pad = NormalizePad(pad)
	if err := ValidatePad(pad); err != nil {
		return "", "", err
	}
	return code, pad, nil
}

func readRecentEntries(path string) ([]RecentEntry, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var stored recentFile
	if err := json.NewDecoder(io.LimitReader(file, recentStoreMaxBytes+1)).Decode(&stored); err != nil {
		return nil, fmt.Errorf("read recent notepads: %w", err)
	}
	if stored.Version != recentStoreVersion {
		return nil, fmt.Errorf("unsupported recent notepad version %d", stored.Version)
	}
	if len(stored.Entries) > RecentStoreMaxItems {
		return nil, errors.New("recent notepad list exceeds its item limit")
	}
	seen := map[string]struct{}{}
	for index := range stored.Entries {
		code, pad, err := validateRecentIdentity(stored.Entries[index].Code, stored.Entries[index].Pad)
		if err != nil || stored.Entries[index].LastOpened <= 0 {
			return nil, errors.New("invalid recent notepad entry")
		}
		stored.Entries[index].Code = code
		stored.Entries[index].Pad = pad
		key := code + "\x00" + pad
		if _, ok := seen[key]; ok {
			return nil, errors.New("duplicate recent notepad entry")
		}
		seen[key] = struct{}{}
	}
	return stored.Entries, nil
}

func sortRecentEntries(entries []RecentEntry) {
	sort.SliceStable(entries, func(left, right int) bool {
		if entries[left].Favorite != entries[right].Favorite {
			return entries[left].Favorite
		}
		if entries[left].LastOpened != entries[right].LastOpened {
			return entries[left].LastOpened > entries[right].LastOpened
		}
		if entries[left].Code != entries[right].Code {
			return entries[left].Code < entries[right].Code
		}
		return entries[left].Pad < entries[right].Pad
	})
}
