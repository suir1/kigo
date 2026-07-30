package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMessageMarshalKeepsZeroChunkCoordinates(t *testing.T) {
	payload, err := json.Marshal(Message{Type: "chunk", Item: 0, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"item":0`) {
		t.Fatalf("zero item index was omitted: %s", payload)
	}
	if !strings.Contains(string(payload), `"offset":0`) {
		t.Fatalf("zero offset was omitted: %s", payload)
	}
}

func TestMessageMarshalKeepsZeroStreamWhenPresent(t *testing.T) {
	stream := 0
	payload, err := json.Marshal(Message{Type: "chunk", Item: 0, Stream: &stream, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"stream":0`) {
		t.Fatalf("zero stream index was omitted: %s", payload)
	}
}

func TestResumeEntryMarshalKeepsZeroStreamWhenPresent(t *testing.T) {
	stream := 0
	payload, err := json.Marshal(ResumeEntry{Item: 0, Stream: &stream, Offset: 0})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"stream":0`) {
		t.Fatalf("zero resume stream was omitted: %s", payload)
	}
}

func TestOptionalCompletedFingerprintFieldsMarshal(t *testing.T) {
	payload, err := json.Marshal(Item{SampleSHA256: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"sample_sha256":"`+strings.Repeat("a", 64)+`"`) {
		t.Fatalf("sample fingerprint was omitted: %s", payload)
	}
	payload, err = json.Marshal(ResumeEntry{SHA256: strings.Repeat("b", 64), Skip: true, Complete: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"sha256":"`+strings.Repeat("b", 64)+`"`) || !strings.Contains(string(payload), `"skip":true`) || !strings.Contains(string(payload), `"complete":true`) {
		t.Fatalf("completed skip fields were omitted: %s", payload)
	}
	if !ValidSHA256(strings.Repeat("A", 64)) || ValidSHA256("bad") {
		t.Fatal("SHA-256 validation rejected hex or accepted an invalid length")
	}
}
