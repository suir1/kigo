package note

import (
	"strings"
	"testing"
	"time"
)

func TestWorkspaceUsesMonotonicRevisionsAndDeterministicConflicts(t *testing.T) {
	workspace := NewWorkspace()
	first, err := workspace.Update(DefaultPad, "first", time.UnixMilli(100))
	if err != nil {
		t.Fatal(err)
	}
	second, err := workspace.Update(DefaultPad, "second", time.UnixMilli(100))
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision != 1 || second.Revision != 2 || second.Timestamp != 101 {
		t.Fatalf("documents = %#v %#v", first, second)
	}
	applied, current, err := workspace.ApplyRemote(Document{
		Pad:       DefaultPad,
		Text:      "remote",
		Revision:  1,
		Timestamp: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if applied || current.Text != "second" {
		t.Fatalf("stale remote update applied=%v current=%#v", applied, current)
	}
	applied, current, err = workspace.ApplyRemote(Document{
		Pad:       DefaultPad,
		Text:      "remote",
		Revision:  3,
		Timestamp: 200,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !applied || current.Text != "remote" {
		t.Fatalf("new remote update applied=%v current=%#v", applied, current)
	}
}

func TestFrameRejectsOversizedText(t *testing.T) {
	frame := Frame{
		Type:      FrameUpdate,
		Version:   ProtocolVersion,
		Pad:       DefaultPad,
		Revision:  1,
		Timestamp: 1,
		Text:      strings.Repeat("x", MaxTextSize+1),
	}
	if err := frame.Validate(); err == nil {
		t.Fatal("expected oversized text to be rejected")
	}
}
