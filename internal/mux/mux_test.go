package mux

import (
	"testing"

	"github.com/suir1/kigo/internal/protocol"
)

func TestPlanUsesIndependentStreamIDs(t *testing.T) {
	plan := NewPlan(2)
	manifest := protocol.NewManifest([]protocol.Item{{Name: "a"}, {Name: "b"}})
	plan.Apply(&manifest)

	got, err := PlanFromManifest(&manifest)
	if err != nil {
		t.Fatal(err)
	}
	if stream, _ := got.StreamForItem(0); stream != 1 {
		t.Fatalf("item 0 stream = %d, want 1", stream)
	}
	if item, _ := got.ItemForStream(2); item != 1 {
		t.Fatalf("stream 2 item = %d, want 1", item)
	}
}

func TestPlanFallsBackToLegacyItemStreams(t *testing.T) {
	manifest := protocol.NewManifest([]protocol.Item{{Name: "a"}, {Name: "b"}})
	plan, err := PlanFromManifest(&manifest)
	if err != nil {
		t.Fatal(err)
	}
	if stream, _ := plan.StreamForItem(1); stream != 1 {
		t.Fatalf("legacy item 1 stream = %d, want 1", stream)
	}
}

func TestTrackerResolvesMappedFrames(t *testing.T) {
	manifest := protocol.NewManifest([]protocol.Item{{Name: "a"}})
	manifest.Streams = []protocol.StreamBinding{{ID: 7, Item: 0}}
	plan, err := PlanFromManifest(&manifest)
	if err != nil {
		t.Fatal(err)
	}
	tracker := NewTracker(plan)
	stream := 7
	binding, err := tracker.AcceptOpen(&manifest, protocol.Message{Type: "stream_open", Item: 0, Stream: &stream})
	if err != nil {
		t.Fatal(err)
	}
	if binding.ID != 7 || binding.Item != 0 {
		t.Fatalf("unexpected binding: %#v", binding)
	}
	if _, err := tracker.AcceptEnd(&manifest, protocol.Message{Type: "stream_end", Item: 0, Stream: &stream}); err != nil {
		t.Fatal(err)
	}
	if _, err := tracker.AcceptChunk(&manifest, protocol.Message{Type: "chunk", Item: 0, Stream: &stream}); err == nil {
		t.Fatal("chunk after stream_end was accepted")
	}
	if _, err := tracker.AcceptChunkAfterEnd(&manifest, protocol.Message{Type: "chunk", Item: 0, Stream: &stream}); err != nil {
		t.Fatalf("striped late chunk rejected: %v", err)
	}
}

func TestTrackerRejectsInvalidStateTransitions(t *testing.T) {
	manifest := protocol.NewManifest([]protocol.Item{{Name: "a"}})
	manifest.Streams = []protocol.StreamBinding{{ID: 7, Item: 0}}
	plan, err := PlanFromManifest(&manifest)
	if err != nil {
		t.Fatal(err)
	}
	stream := 7
	tracker := NewTracker(plan)
	if _, err := tracker.AcceptOpen(&manifest, protocol.Message{Type: "stream_open", Item: 0, Stream: &stream}); err != nil {
		t.Fatal(err)
	}
	if _, err := tracker.AcceptOpen(&manifest, protocol.Message{Type: "stream_open", Item: 0, Stream: &stream}); err == nil {
		t.Fatal("duplicate stream_open was accepted")
	}

	tracker = NewTracker(plan)
	if _, err := tracker.AcceptEnd(&manifest, protocol.Message{Type: "stream_end", Item: 0, Stream: &stream}); err == nil {
		t.Fatal("stream_end before open was accepted")
	}
}

func TestValidateResumeEntryUsesMappedStream(t *testing.T) {
	items := []protocol.Item{{Kind: protocol.ItemFile, Name: "a", Size: 10}}
	manifest := protocol.NewManifest(items)
	manifest.Streams = []protocol.StreamBinding{{ID: 7, Item: 0}}
	plan, err := PlanFromManifest(&manifest)
	if err != nil {
		t.Fatal(err)
	}
	stream := 7
	offset, err := plan.ValidateResumeEntry(items, protocol.ResumeEntry{Item: 0, Stream: &stream, Offset: 99})
	if err != nil {
		t.Fatal(err)
	}
	if offset != 10 {
		t.Fatalf("offset = %d, want 10", offset)
	}
	badStream := 0
	if _, err := plan.ValidateResumeEntry(items, protocol.ResumeEntry{Item: 0, Stream: &badStream}); err == nil {
		t.Fatal("mismatched resume stream was accepted")
	}
	if _, err := plan.ValidateResumeEntry(items, protocol.ResumeEntry{Item: 0, Stream: &stream, Offset: 1, PrefixSHA256: "bad"}); err == nil {
		t.Fatal("invalid resume prefix hash was accepted")
	}
	if _, err := plan.ValidateResumeEntry(items, protocol.ResumeEntry{Item: 0, Stream: &stream, SHA256: "bad"}); err == nil {
		t.Fatal("invalid resume file hash was accepted")
	}
	if _, err := plan.ValidateResumeEntry(items, protocol.ResumeEntry{Item: 0, Stream: &stream, Offset: 99, Skip: true}); err == nil {
		t.Fatal("skip with clamped offset was accepted")
	}
	if offset, err := plan.ValidateResumeEntry(items, protocol.ResumeEntry{Item: 0, Stream: &stream, Offset: 10, Skip: true}); err != nil || offset != 10 {
		t.Fatalf("valid skip rejected: offset=%d err=%v", offset, err)
	}
	if _, err := plan.ValidateResumeEntry(items, protocol.ResumeEntry{Item: 0, Stream: &stream, Offset: 10, Complete: true}); err == nil {
		t.Fatal("completed resume without skip was accepted")
	}
	if offset, err := plan.ValidateResumeEntry(items, protocol.ResumeEntry{Item: 0, Stream: &stream, Offset: 10, Skip: true, Complete: true}); err != nil || offset != 10 {
		t.Fatalf("valid completed skip rejected: offset=%d err=%v", offset, err)
	}
}

func TestPlanRejectsDuplicateBindings(t *testing.T) {
	manifest := protocol.NewManifest([]protocol.Item{{Name: "a"}, {Name: "b"}})
	manifest.Streams = []protocol.StreamBinding{{ID: 3, Item: 0}, {ID: 3, Item: 1}}
	if _, err := PlanFromManifest(&manifest); err == nil {
		t.Fatal("duplicate stream id was accepted")
	}
}
