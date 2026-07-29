package note

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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

func TestPersistentSessionDoesNotRepublishEqualSnapshot(t *testing.T) {
	const code = "PROJECT-ALPHA-2026"
	document := Document{Pad: "main", Text: "already synchronized", Revision: 3, Timestamp: 1234}
	record, err := SealPersistentDocument(code, document)
	if err != nil {
		t.Fatal(err)
	}
	received := make(chan PersistentMessage, 1)
	handlerErrors := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			handlerErrors <- err
			return
		}
		defer conn.Close()
		if err := conn.WriteJSON(PersistentMessage{
			Type: PersistentState, Version: PersistentProtocolVersion, Generation: 1, Record: &record,
		}); err != nil {
			handlerErrors <- err
			return
		}
		var message PersistentMessage
		if err := conn.ReadJSON(&message); err == nil {
			received <- message
		}
	}))
	defer server.Close()

	workspace := NewWorkspace()
	if _, _, err := workspace.ApplyRemote(document); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	session, err := OpenPersistentSession(ctx, PersistentOptions{ServiceBase: server.URL, Code: code, Pad: "main"})
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	if applied, err := session.SyncWorkspace(ctx, workspace, "main"); err != nil {
		t.Fatal(err)
	} else if len(applied) != 0 {
		t.Fatalf("equal snapshot was applied again: %#v", applied)
	}

	select {
	case err := <-handlerErrors:
		t.Fatal(err)
	case message := <-received:
		t.Fatalf("equal snapshot was republished: %#v", message)
	case <-time.After(150 * time.Millisecond):
	}
}
