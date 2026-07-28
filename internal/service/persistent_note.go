package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/suir1/kigo/internal/note"
)

const (
	defaultPersistentNoteTTL  = 30 * 24 * time.Hour
	persistentNoteMaxClients  = 16
	persistentNoteDiskVersion = 1
)

type persistentNoteHub struct {
	key        string
	generation uint64
	record     *note.PersistentRecord
	updated    time.Time
	expires    time.Time
	clients    map[*persistentNoteClient]struct{}
	deliveryMu sync.Mutex
}

type persistentNoteClient struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

type persistentNoteDiskRecord struct {
	Version    int                   `json:"version"`
	Generation uint64                `json:"generation"`
	UpdatedAt  int64                 `json:"updated_at"`
	ExpiresAt  int64                 `json:"expires_at"`
	Record     note.PersistentRecord `json:"record"`
}

func (s *Server) handlePersistentNote(w http.ResponseWriter, r *http.Request) {
	if !s.allowRequest(r) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	roomToken, padToken, ok := persistentNotePath(r.URL.Path)
	if !ok || !isRoomToken(roomToken) {
		http.Error(w, "invalid persistent notepad identity", http.StatusBadRequest)
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(note.MaxPersistentMessageSize)
	client := &persistentNoteClient{conn: conn}
	hub, state, err := s.joinPersistentNote(roomToken+"\x00"+padToken, client)
	if err != nil {
		_ = client.write(note.PersistentMessage{Type: note.PersistentError, Version: note.PersistentProtocolVersion, Error: err.Error()})
		_ = conn.Close()
		return
	}
	defer func() {
		s.leavePersistentNote(hub, client)
		_ = conn.Close()
	}()
	if err := client.write(state); err != nil {
		return
	}

	done := make(chan struct{})
	defer close(done)
	go client.pingLoop(done)
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var message note.PersistentMessage
		if err := json.Unmarshal(payload, &message); err != nil {
			_ = client.writeError("decode persistent note message")
			continue
		}
		if err := message.ValidateClient(); err != nil {
			_ = client.writeError(err.Error())
			continue
		}
		if err := s.applyPersistentNote(hub, client, message); err != nil {
			_ = client.writeError(err.Error())
		}
	}
}

func persistentNotePath(path string) (string, string, bool) {
	value := strings.Trim(strings.TrimPrefix(path, "/api/note-sync/"), "/")
	parts := strings.Split(value, "/")
	if len(parts) != 2 || len(parts[1]) != sha256.Size*2 {
		return "", "", false
	}
	if _, err := hex.DecodeString(parts[1]); err != nil || strings.ToLower(parts[1]) != parts[1] {
		return "", "", false
	}
	return parts[0], parts[1], true
}

func (s *Server) joinPersistentNote(key string, client *persistentNoteClient) (*persistentNoteHub, note.PersistentMessage, error) {
	s.noteMu.Lock()
	defer s.noteMu.Unlock()
	hub := s.notes[key]
	if hub == nil {
		loaded, err := s.loadPersistentNote(key, time.Now())
		if err != nil {
			return nil, note.PersistentMessage{}, err
		}
		hub = loaded
		if hub == nil {
			hub = &persistentNoteHub{key: key, clients: map[*persistentNoteClient]struct{}{}}
		}
		s.notes[key] = hub
	}
	if len(hub.clients) >= persistentNoteMaxClients {
		return nil, note.PersistentMessage{}, errors.New("persistent notepad has too many connected clients")
	}
	hub.clients[client] = struct{}{}
	return hub, persistentNoteState(hub), nil
}

func (s *Server) leavePersistentNote(hub *persistentNoteHub, client *persistentNoteClient) {
	if hub == nil || client == nil {
		return
	}
	s.noteMu.Lock()
	defer s.noteMu.Unlock()
	delete(hub.clients, client)
	if len(hub.clients) == 0 && hub.record == nil {
		delete(s.notes, hub.key)
	}
}

func (s *Server) applyPersistentNote(hub *persistentNoteHub, client *persistentNoteClient, message note.PersistentMessage) error {
	hub.deliveryMu.Lock()
	defer hub.deliveryMu.Unlock()

	s.noteMu.Lock()
	if current := s.notes[hub.key]; current != hub {
		s.noteMu.Unlock()
		return errors.New("persistent notepad expired")
	}
	if message.BaseGeneration != hub.generation {
		state := persistentNoteState(hub)
		s.noteMu.Unlock()
		return client.write(state)
	}
	now := time.Now()
	record := clonePersistentRecord(*message.Record)
	next := persistentNoteDiskRecord{
		Version:    persistentNoteDiskVersion,
		Generation: hub.generation + 1,
		UpdatedAt:  now.UnixMilli(),
		ExpiresAt:  now.Add(s.cfg.NoteTTL).UnixMilli(),
		Record:     record,
	}
	if err := s.writePersistentNote(hub.key, next); err != nil {
		s.noteMu.Unlock()
		return fmt.Errorf("persist encrypted notepad: %w", err)
	}
	hub.generation = next.Generation
	hub.record = &record
	hub.updated = now
	hub.expires = time.UnixMilli(next.ExpiresAt)
	state := persistentNoteState(hub)
	clients := make([]*persistentNoteClient, 0, len(hub.clients))
	for connected := range hub.clients {
		clients = append(clients, connected)
	}
	s.noteMu.Unlock()

	var senderErr error
	var senderErrMu sync.Mutex
	var broadcasts sync.WaitGroup
	for _, connected := range clients {
		broadcasts.Add(1)
		go func(target *persistentNoteClient) {
			defer broadcasts.Done()
			if err := target.write(state); err != nil {
				_ = target.conn.Close()
				if target == client {
					senderErrMu.Lock()
					senderErr = err
					senderErrMu.Unlock()
				}
			}
		}(connected)
	}
	broadcasts.Wait()
	return senderErr
}

func persistentNoteState(hub *persistentNoteHub) note.PersistentMessage {
	message := note.PersistentMessage{
		Type:       note.PersistentState,
		Version:    note.PersistentProtocolVersion,
		Generation: hub.generation,
	}
	if hub.record != nil {
		record := clonePersistentRecord(*hub.record)
		message.Record = &record
	}
	return message
}

func clonePersistentRecord(record note.PersistentRecord) note.PersistentRecord {
	record.Salt = append([]byte(nil), record.Salt...)
	record.Nonce = append([]byte(nil), record.Nonce...)
	record.Ciphertext = append([]byte(nil), record.Ciphertext...)
	return record
}

func (c *persistentNoteClient) write(message note.PersistentMessage) error {
	if c == nil || c.conn == nil {
		return websocket.ErrCloseSent
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(websocketWriteWait))
	return c.conn.WriteJSON(message)
}

func (c *persistentNoteClient) writeError(message string) error {
	return c.write(note.PersistentMessage{
		Type: note.PersistentError, Version: note.PersistentProtocolVersion, Error: message,
	})
}

func (c *persistentNoteClient) pingLoop(done <-chan struct{}) {
	ticker := time.NewTicker(websocketPingPeriod)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			c.mu.Lock()
			_ = c.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(websocketWriteWait))
			c.mu.Unlock()
		}
	}
}

func (s *Server) persistentNoteStats() map[string]any {
	s.noteMu.Lock()
	defer s.noteMu.Unlock()
	documents := 0
	clients := 0
	for _, hub := range s.notes {
		if hub.record != nil {
			documents++
		}
		clients += len(hub.clients)
	}
	return map[string]any{
		"configured": s.cfg.NoteStore != "",
		"documents":  documents,
		"clients":    clients,
		"ttl_ms":     s.cfg.NoteTTL.Milliseconds(),
	}
}

func (s *Server) persistentNotePath(key string) string {
	if strings.TrimSpace(s.cfg.NoteStore) == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("kigo-persistent-note:" + key))
	return filepath.Join(filepath.Clean(s.cfg.NoteStore), hex.EncodeToString(sum[:])+".json")
}

func (s *Server) loadPersistentNote(key string, now time.Time) (*persistentNoteHub, error) {
	path := s.persistentNotePath(key)
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var record persistentNoteDiskRecord
	if err := json.NewDecoder(io.LimitReader(file, note.MaxPersistentMessageSize+1)).Decode(&record); err != nil {
		return nil, err
	}
	if err := validatePersistentNoteDiskRecord(record); err != nil {
		return nil, err
	}
	if record.ExpiresAt <= now.UnixMilli() {
		_ = os.Remove(path)
		return nil, nil
	}
	sealed := clonePersistentRecord(record.Record)
	return &persistentNoteHub{
		key:        key,
		generation: record.Generation,
		record:     &sealed,
		updated:    time.UnixMilli(record.UpdatedAt),
		expires:    time.UnixMilli(record.ExpiresAt),
		clients:    map[*persistentNoteClient]struct{}{},
	}, nil
}

func (s *Server) writePersistentNote(key string, record persistentNoteDiskRecord) error {
	path := s.persistentNotePath(key)
	if path == "" {
		return nil
	}
	if err := validatePersistentNoteDiskRecord(record); err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(dir, ".note-*.tmp")
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
		_ = os.Remove(tempPath)
		return err
	}
	return nil
}

func validatePersistentNoteDiskRecord(record persistentNoteDiskRecord) error {
	if record.Version != persistentNoteDiskVersion || record.Generation == 0 || record.UpdatedAt <= 0 || record.ExpiresAt <= record.UpdatedAt {
		return errors.New("invalid persistent notepad disk record")
	}
	return record.Record.Validate()
}

func (s *Server) cleanupPersistentNotes(now time.Time) {
	s.noteMu.Lock()
	defer s.noteMu.Unlock()
	for key, hub := range s.notes {
		if len(hub.clients) != 0 || hub.record == nil || now.Before(hub.expires) {
			continue
		}
		delete(s.notes, key)
		if path := s.persistentNotePath(key); path != "" {
			_ = os.Remove(path)
		}
	}
	s.cleanupPersistentNoteFiles(now)
}

func (s *Server) cleanupPersistentNoteFiles(now time.Time) {
	path := strings.TrimSpace(s.cfg.NoteStore)
	if path == "" {
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		filePath := filepath.Join(path, entry.Name())
		file, err := os.Open(filePath)
		if err != nil {
			continue
		}
		var record persistentNoteDiskRecord
		err = json.NewDecoder(io.LimitReader(file, note.MaxPersistentMessageSize+1)).Decode(&record)
		_ = file.Close()
		if err == nil && record.ExpiresAt > 0 && record.ExpiresAt <= now.UnixMilli() {
			_ = os.Remove(filePath)
		}
	}
}
