package note

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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

	"github.com/suir1/kigo/internal/secure"
	"golang.org/x/crypto/hkdf"
)

const (
	DraftTTL           = 7 * 24 * time.Hour
	draftStoreVersion  = 1
	draftRecordMaxSize = 2 << 20
	draftStoreMaxDocs  = 16
	draftKeyInfo       = "kigo-note-draft-v1"
)

type DraftStore struct {
	path string
	now  func() time.Time
}

type draftRecord struct {
	Version    int    `json:"version"`
	UpdatedAt  int64  `json:"updated_at"`
	ExpiresAt  int64  `json:"expires_at"`
	Salt       []byte `json:"salt"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

var draftFileMu sync.Mutex

func NewDraftStore(path string) *DraftStore {
	return &DraftStore{path: filepath.Clean(path), now: time.Now}
}

func (s *DraftStore) Load(code, role, pad string) (Document, bool, error) {
	if s == nil || strings.TrimSpace(s.path) == "" || s.path == "." {
		return Document{}, false, nil
	}
	code, pad, err := validateDraftIdentity(code, role, pad)
	if err != nil {
		return Document{}, false, err
	}
	record, err := readDraftRecord(s.entryPath(code, role, pad))
	if errors.Is(err, os.ErrNotExist) {
		return Document{}, false, nil
	}
	if err != nil {
		return Document{}, false, err
	}
	if record.ExpiresAt <= s.clock().UnixMilli() {
		return Document{}, false, nil
	}
	document, err := openDraftRecord(code, role, pad, record)
	if err != nil {
		return Document{}, false, fmt.Errorf("decrypt note draft: %w", err)
	}
	if document.Pad != pad {
		return Document{}, false, errors.New("note draft pad does not match its storage identity")
	}
	if err := ValidateDocument(document); err != nil {
		return Document{}, false, fmt.Errorf("validate note draft: %w", err)
	}
	return document, true, nil
}

func (s *DraftStore) Save(code, role string, document Document) error {
	if s == nil || strings.TrimSpace(s.path) == "" || s.path == "." || document.Revision == 0 {
		return nil
	}
	code, pad, err := validateDraftIdentity(code, role, document.Pad)
	if err != nil {
		return err
	}
	document.Pad = pad
	if err := ValidateDocument(document); err != nil {
		return err
	}
	now := s.clock()
	record, err := sealDraftRecord(code, role, document, now)
	if err != nil {
		return err
	}
	path := s.entryPath(code, role, pad)
	draftFileMu.Lock()
	defer draftFileMu.Unlock()
	if err := withDraftFileLock(path, func() error { return writeDraftRecord(path, record) }); err != nil {
		return err
	}
	pruneDraftDirectory(s.path, now)
	return nil
}

func (s *DraftStore) entryPath(code, role, pad string) string {
	return filepath.Join(s.path, draftEntryKey(code, role, pad)+".json")
}

func (s *DraftStore) clock() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

func validateDraftIdentity(code, role, pad string) (string, string, error) {
	code, err := secure.ValidateCode(code)
	if err != nil {
		return "", "", err
	}
	switch role {
	case "host", "join":
	default:
		return "", "", fmt.Errorf("invalid note draft role %q", role)
	}
	pad = NormalizePad(pad)
	if err := ValidatePad(pad); err != nil {
		return "", "", err
	}
	return code, pad, nil
}

func draftEntryKey(code, role, pad string) string {
	identity := secure.RoomToken(code) + "\x00" + role + "\x00" + pad
	sum := sha256.Sum256([]byte("kigo-note-draft-key:" + identity))
	return hex.EncodeToString(sum[:])
}

func sealDraftRecord(code, role string, document Document, now time.Time) (draftRecord, error) {
	plaintext, err := json.Marshal(document)
	if err != nil {
		return draftRecord{}, err
	}
	salt := make([]byte, 16)
	nonce := make([]byte, 12)
	if _, err := rand.Read(salt); err != nil {
		return draftRecord{}, err
	}
	if _, err := rand.Read(nonce); err != nil {
		return draftRecord{}, err
	}
	aead, err := draftAEAD(code, salt)
	if err != nil {
		return draftRecord{}, err
	}
	return draftRecord{
		Version:    draftStoreVersion,
		UpdatedAt:  now.UnixMilli(),
		ExpiresAt:  now.Add(DraftTTL).UnixMilli(),
		Salt:       salt,
		Nonce:      nonce,
		Ciphertext: aead.Seal(nil, nonce, plaintext, draftAAD(role, document.Pad)),
	}, nil
}

func openDraftRecord(code, role, pad string, record draftRecord) (Document, error) {
	aead, err := draftAEAD(code, record.Salt)
	if err != nil {
		return Document{}, err
	}
	if len(record.Nonce) != aead.NonceSize() {
		return Document{}, errors.New("invalid note draft nonce")
	}
	plaintext, err := aead.Open(nil, record.Nonce, record.Ciphertext, draftAAD(role, pad))
	if err != nil {
		return Document{}, err
	}
	var document Document
	if err := json.Unmarshal(plaintext, &document); err != nil {
		return Document{}, err
	}
	return document, nil
}

func draftAEAD(code string, salt []byte) (cipher.AEAD, error) {
	key := make([]byte, 16)
	if _, err := io.ReadFull(hkdf.New(sha256.New, []byte(secure.NormalizeCode(code)), salt, []byte(draftKeyInfo)), key); err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func draftAAD(role, pad string) []byte {
	return []byte(draftKeyInfo + "\x00" + role + "\x00" + pad)
}

func readDraftRecord(path string) (draftRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return draftRecord{}, err
	}
	defer file.Close()
	var record draftRecord
	if err := json.NewDecoder(io.LimitReader(file, draftRecordMaxSize+1)).Decode(&record); err != nil {
		return draftRecord{}, fmt.Errorf("read note draft: %w", err)
	}
	if record.Version != draftStoreVersion {
		return draftRecord{}, fmt.Errorf("unsupported note draft version %d", record.Version)
	}
	return record, nil
}

func writeDraftRecord(path string, record draftRecord) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".note-draft-*.tmp")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	cleanup := func() {
		_ = file.Close()
		_ = os.Remove(tempPath)
	}
	if err := file.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if err := json.NewEncoder(file).Encode(record); err != nil {
		cleanup()
		return err
	}
	if err := file.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tempPath)
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			_ = os.Remove(tempPath)
			return err
		}
		if retryErr := os.Rename(tempPath, path); retryErr != nil {
			_ = os.Remove(tempPath)
			return retryErr
		}
	}
	return nil
}

func pruneDraftDirectory(path string, now time.Time) {
	entries, err := os.ReadDir(path)
	if err != nil {
		return
	}
	type entryAge struct {
		path string
		at   int64
	}
	ages := make([]entryAge, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		entryPath := filepath.Join(path, entry.Name())
		record, err := readDraftRecord(entryPath)
		if err != nil {
			continue
		}
		if record.ExpiresAt <= now.UnixMilli() {
			_ = os.Remove(entryPath)
			continue
		}
		ages = append(ages, entryAge{path: entryPath, at: record.UpdatedAt})
	}
	sort.Slice(ages, func(i, j int) bool { return ages[i].at < ages[j].at })
	for len(ages) > draftStoreMaxDocs {
		_ = os.Remove(ages[0].path)
		ages = ages[1:]
	}
}

func withDraftFileLock(path string, fn func() error) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lockPath := path + ".lock"
	deadline := time.Now().Add(2 * time.Second)
	for {
		lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			_ = lock.Close()
			defer os.Remove(lockPath)
			return fn()
		}
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > 30*time.Second {
			_ = os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return errors.New("note draft lock timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
