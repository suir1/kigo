package note

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	DefaultPad  = "main"
	MaxTextSize = 1 << 20
	MaxPadSize  = 64
)

type Document struct {
	Pad       string `json:"pad"`
	Text      string `json:"text"`
	Revision  uint64 `json:"revision"`
	Timestamp int64  `json:"timestamp"`
}

type Workspace struct {
	mu   sync.RWMutex
	docs map[string]Document
}

func NewWorkspace() *Workspace {
	return &Workspace{docs: make(map[string]Document)}
}

func (w *Workspace) Snapshot(pad string) Document {
	pad = NormalizePad(pad)
	w.mu.RLock()
	defer w.mu.RUnlock()
	if document, ok := w.docs[pad]; ok {
		return document
	}
	return Document{Pad: pad}
}

func (w *Workspace) Update(pad, text string, now time.Time) (Document, error) {
	pad = NormalizePad(pad)
	if err := ValidatePad(pad); err != nil {
		return Document{}, err
	}
	if err := ValidateText(text); err != nil {
		return Document{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	current := w.docs[pad]
	timestamp := now.UnixMilli()
	if timestamp <= current.Timestamp {
		timestamp = current.Timestamp + 1
	}
	document := Document{
		Pad:       pad,
		Text:      text,
		Revision:  current.Revision + 1,
		Timestamp: timestamp,
	}
	w.docs[pad] = document
	return document, nil
}

func (w *Workspace) Clear(pad string, now time.Time) (Document, error) {
	return w.Update(pad, "", now)
}

func (w *Workspace) ApplyRemote(document Document) (bool, Document, error) {
	document.Pad = NormalizePad(document.Pad)
	if err := ValidateDocument(document); err != nil {
		return false, Document{}, err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	current := w.docs[document.Pad]
	if compareDocuments(document, current) <= 0 {
		return false, currentDocument(document.Pad, current), nil
	}
	w.docs[document.Pad] = document
	return true, document, nil
}

func NormalizePad(pad string) string {
	pad = strings.TrimSpace(pad)
	if pad == "" {
		return DefaultPad
	}
	return pad
}

func ValidatePad(pad string) error {
	if pad == "" {
		return errors.New("note pad is empty")
	}
	if len([]byte(pad)) > MaxPadSize {
		return fmt.Errorf("note pad exceeds %d bytes", MaxPadSize)
	}
	if strings.ContainsAny(pad, "\x00\r\n") {
		return errors.New("note pad contains unsupported characters")
	}
	return nil
}

func ValidateText(text string) error {
	if len([]byte(text)) > MaxTextSize {
		return fmt.Errorf("note text exceeds %d bytes", MaxTextSize)
	}
	return nil
}

func ValidateDocument(document Document) error {
	if err := ValidatePad(document.Pad); err != nil {
		return err
	}
	if err := ValidateText(document.Text); err != nil {
		return err
	}
	if document.Revision == 0 {
		return errors.New("note document revision must be positive")
	}
	if document.Timestamp <= 0 {
		return errors.New("note document timestamp must be positive")
	}
	return nil
}

func compareDocuments(left, right Document) int {
	switch {
	case left.Revision < right.Revision:
		return -1
	case left.Revision > right.Revision:
		return 1
	case left.Timestamp < right.Timestamp:
		return -1
	case left.Timestamp > right.Timestamp:
		return 1
	case left.Text < right.Text:
		return -1
	case left.Text > right.Text:
		return 1
	default:
		return 0
	}
}

func currentDocument(pad string, document Document) Document {
	if document.Pad == "" {
		document.Pad = pad
	}
	return document
}
