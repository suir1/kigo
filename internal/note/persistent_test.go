package note

import (
	"strings"
	"testing"
)

func TestPersistentDocumentRoundTrip(t *testing.T) {
	document := Document{Pad: "roadmap", Text: "ship it", Revision: 7, Timestamp: 1234}
	record, err := SealPersistentDocument("PROJECT-ALPHA-2026", document)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := OpenPersistentDocument("project-alpha-2026", "roadmap", record)
	if err != nil {
		t.Fatal(err)
	}
	if opened != document {
		t.Fatalf("opened document = %#v, want %#v", opened, document)
	}
}

func TestPersistentDocumentAuthenticatesCodeAndPad(t *testing.T) {
	record, err := SealPersistentDocument("PROJECT-ALPHA-2026", Document{
		Pad: "main", Text: "secret", Revision: 1, Timestamp: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPersistentDocument("PROJECT-BRAVO-2026", "main", record); err == nil {
		t.Fatal("wrong code decrypted persistent note")
	}
	if _, err := OpenPersistentDocument("PROJECT-ALPHA-2026", "other", record); err == nil {
		t.Fatal("wrong pad decrypted persistent note")
	}
}

func TestPersistentPadTokenIsStableAndOpaque(t *testing.T) {
	first, err := PersistentPadToken(" roadmap ")
	if err != nil {
		t.Fatal(err)
	}
	second, err := PersistentPadToken("roadmap")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != 64 || strings.Contains(first, "roadmap") {
		t.Fatalf("unexpected pad token %q / %q", first, second)
	}
}

func TestPersistentMessageRejectsInvalidClientInput(t *testing.T) {
	valid := PersistentMessage{
		Type:    PersistentPut,
		Version: PersistentProtocolVersion,
		Record: &PersistentRecord{
			Version: PersistentRecordVersion, Salt: make([]byte, 16), Nonce: make([]byte, 12), Ciphertext: make([]byte, 16),
		},
	}
	if err := valid.ValidateClient(); err != nil {
		t.Fatal(err)
	}
	valid.Type = PersistentState
	if err := valid.ValidateClient(); err == nil {
		t.Fatal("server state accepted as client input")
	}
}
