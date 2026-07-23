package note

import (
	"errors"
	"fmt"
	"strings"
)

const (
	ProtocolVersion = 1

	FrameUpdate = "update"
	FrameClear  = "clear"
	FrameAck    = "ack"
	FramePing   = "ping"
	FramePong   = "pong"
	FrameBye    = "bye"
)

type Frame struct {
	Type      string `json:"type"`
	Version   int    `json:"version"`
	Pad       string `json:"pad,omitempty"`
	Revision  uint64 `json:"revision,omitempty"`
	Timestamp int64  `json:"timestamp,omitempty"`
	Text      string `json:"text,omitempty"`
}

func (f Frame) Validate() error {
	if f.Version != ProtocolVersion {
		return fmt.Errorf("unsupported note frame version %d", f.Version)
	}
	switch f.Type {
	case FrameUpdate, FrameClear:
		document := f.Document()
		if err := ValidateDocument(document); err != nil {
			return err
		}
	case FrameAck:
		if err := ValidatePad(NormalizePad(f.Pad)); err != nil {
			return err
		}
		if f.Revision == 0 {
			return errors.New("note ack revision must be positive")
		}
	case FramePing, FramePong, FrameBye:
		if f.Pad != "" {
			if err := ValidatePad(NormalizePad(f.Pad)); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported note frame type %q", f.Type)
	}
	return nil
}

func (f Frame) Document() Document {
	return Document{
		Pad:       NormalizePad(f.Pad),
		Text:      f.Text,
		Revision:  f.Revision,
		Timestamp: f.Timestamp,
	}
}

func FrameFromDocument(frameType string, document Document) Frame {
	return Frame{
		Type:      frameType,
		Version:   ProtocolVersion,
		Pad:       document.Pad,
		Revision:  document.Revision,
		Timestamp: document.Timestamp,
		Text:      document.Text,
	}
}

func (f Frame) IsDocumentUpdate() bool {
	return f.Type == FrameUpdate || f.Type == FrameClear
}

func (f Frame) String() string {
	if f.Type == FrameUpdate || f.Type == FrameClear {
		return fmt.Sprintf("%s %s revision %d", f.Type, strings.TrimSpace(f.Pad), f.Revision)
	}
	return f.Type
}
