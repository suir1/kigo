package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/suir1/kigo/internal/relay"
)

const negotiationTestToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func nativeCapability(relay string, local bool) negotiationCapability {
	return negotiationCapability{
		Type:        "negotiate",
		Version:     negotiationVersion,
		Client:      "native",
		NativeRelay: relay,
		NativeLocal: local,
	}
}

func webCapability() negotiationCapability {
	return negotiationCapability{
		Type:    "negotiate",
		Version: negotiationVersion,
		Client:  "web",
	}
}

func TestJoinNegotiationRejectsDuplicateActiveRole(t *testing.T) {
	s := New(Config{RoomTTL: time.Minute})
	first, err := s.joinNegotiation(
		negotiationTestToken,
		"sender",
		nativeCapability("", false),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	defer s.leaveNegotiation(negotiationTestToken, first)

	if _, err := s.joinNegotiation(
		negotiationTestToken,
		"sender",
		nativeCapability("", false),
		false,
	); err == nil || !strings.Contains(err.Error(), "already negotiating") {
		t.Fatalf("duplicate role error = %v", err)
	}
}

func TestNoteNegotiationSucceeds(t *testing.T) {
	s := New(Config{RoomTTL: time.Minute})
	senderCapability := nativeCapability("", false)
	senderCapability.Protocol = negotiationNote
	receiverCapability := nativeCapability("", false)
	receiverCapability.Protocol = negotiationNote

	sender, err := s.joinNegotiation(negotiationTestToken, "sender", senderCapability, false)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := s.joinNegotiation(negotiationTestToken, "receiver", receiverCapability, false)
	if err != nil {
		t.Fatal(err)
	}
	for role, peer := range map[string]*negotiationPeer{
		"sender":   sender,
		"receiver": receiver,
	} {
		select {
		case outcome := <-peer.result:
			if outcome.err != nil {
				t.Fatalf("%s negotiation failed: %v", role, outcome.err)
			}
			if outcome.response.Route != "webrtc" {
				t.Fatalf("%s response = %#v", role, outcome.response)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s note negotiation timed out", role)
		}
	}
}

func TestNegotiationRejectsProtocolMismatch(t *testing.T) {
	s := New(Config{RoomTTL: time.Minute})
	senderCapability := nativeCapability("", false)
	senderCapability.Protocol = negotiationNote
	sender, err := s.joinNegotiation(
		negotiationTestToken,
		"sender",
		senderCapability,
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := s.joinNegotiation(
		negotiationTestToken,
		"receiver",
		nativeCapability("", false),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	for role, peer := range map[string]*negotiationPeer{
		"sender":   sender,
		"receiver": receiver,
	} {
		select {
		case outcome := <-peer.result:
			if outcome.err == nil || !strings.Contains(outcome.err.Error(), "protocol mismatch") {
				t.Fatalf("%s mismatch outcome = %#v", role, outcome)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s mismatch negotiation timed out", role)
		}
	}
}

func TestPassiveSignalingPeerSelectsWebRTC(t *testing.T) {
	s := New(Config{RoomTTL: time.Minute, NativeRelay: "relay.example:9000"})
	active, err := s.joinNegotiation(
		negotiationTestToken,
		"sender",
		nativeCapability("", false),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}

	s.observeWebRTCOnly(negotiationTestToken, "receiver")

	select {
	case outcome := <-active.result:
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if outcome.response.Route != "webrtc" {
			t.Fatalf("response = %#v", outcome.response)
		}
	case <-time.After(time.Second):
		t.Fatal("active native peer did not receive negotiation result")
	}
	s.mu.Lock()
	_, exists := s.negotiations.rooms[negotiationTestToken]
	s.mu.Unlock()
	if exists {
		t.Fatal("completed negotiation room was not removed")
	}
}

func TestServiceRelayNegotiationIssuesTemporaryCredential(t *testing.T) {
	s := New(Config{
		RoomTTL:                  time.Minute,
		NativeRelay:              "relay.example:9000",
		NativeRelaySecret:        "relay-secret",
		NativeRelayCredentialTTL: time.Hour,
	})
	sender, err := s.joinNegotiation(
		negotiationTestToken,
		"sender",
		nativeCapability("", false),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := s.joinNegotiation(
		negotiationTestToken,
		"receiver",
		nativeCapability("", false),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	for role, peer := range map[string]*negotiationPeer{
		"sender":   sender,
		"receiver": receiver,
	} {
		select {
		case outcome := <-peer.result:
			if outcome.err != nil {
				t.Fatal(outcome.err)
			}
			if outcome.response.Route != "native" ||
				outcome.response.Relay != "relay.example:9000" ||
				outcome.response.RelayCredential == "" {
				t.Fatalf("%s response = %#v", role, outcome.response)
			}
			if !relay.ValidateCredential(
				"relay-secret",
				negotiationTestToken,
				outcome.response.RelayCredential,
				time.Now(),
			) {
				t.Fatalf("%s received invalid relay credential", role)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s did not receive negotiation result", role)
		}
	}
}

func TestCleanupExpiresNegotiationRoomAndNotifiesWaiter(t *testing.T) {
	s := New(Config{RoomTTL: time.Minute})
	active, err := s.joinNegotiation(
		negotiationTestToken,
		"sender",
		nativeCapability("", false),
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.negotiations.rooms[negotiationTestToken].expires = time.Now().Add(-time.Second)
	s.mu.Unlock()

	s.cleanup()

	select {
	case outcome := <-active.result:
		if outcome.err == nil || !strings.Contains(outcome.err.Error(), "expired") {
			t.Fatalf("expiration outcome = %#v", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("expired negotiation did not notify active waiter")
	}
}

func TestJoinNegotiationRejectsInvalidRoom(t *testing.T) {
	s := New(Config{})
	if _, err := s.joinNegotiation("bad", "sender", nativeCapability("", false), false); err == nil {
		t.Fatal("invalid token was accepted")
	}
	if _, err := s.joinNegotiation(negotiationTestToken, "bad", nativeCapability("", false), false); err == nil {
		t.Fatal("invalid role was accepted")
	}
}

func TestHandleNegotiateRejectsInvalidCapability(t *testing.T) {
	s := New(Config{RoomTTL: time.Minute})
	server := httptest.NewServer(s.handler())
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	wsURL := "ws" + server.URL[len("http"):] + "/api/negotiate/" + negotiationTestToken + "?role=sender"
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.WriteJSON(map[string]any{
		"type":         "negotiate",
		"version":      negotiationVersion,
		"client":       "web",
		"native_local": true,
	}); err != nil {
		t.Fatal(err)
	}
	var response map[string]string
	if err := conn.ReadJSON(&response); err != nil {
		t.Fatal(err)
	}
	if response["type"] != "error" || !strings.Contains(response["error"], "non-native") {
		t.Fatalf("response = %#v", response)
	}

	for _, path := range []string{
		"/api/negotiate/bad?role=sender",
		"/api/negotiate/" + negotiationTestToken + "?role=bad",
	} {
		url := "ws" + server.URL[len("http"):] + path
		rejected, response, err := websocket.DefaultDialer.DialContext(ctx, url, nil)
		if rejected != nil {
			_ = rejected.Close()
		}
		if err == nil || response == nil || response.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s response=%#v err=%v", path, response, err)
		}
	}
}

func TestHandleNegotiateClosesNormallyAfterResult(t *testing.T) {
	s := New(Config{RoomTTL: time.Minute})
	server := httptest.NewServer(s.handler())
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	dial := func(role string) *websocket.Conn {
		t.Helper()
		wsURL := "ws" + server.URL[len("http"):] + "/api/negotiate/" + negotiationTestToken + "?role=" + role
		conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		return conn
	}
	sender := dial("sender")
	defer sender.Close()
	receiver := dial("receiver")
	defer receiver.Close()
	if err := sender.WriteJSON(webCapability()); err != nil {
		t.Fatal(err)
	}
	if err := receiver.WriteJSON(webCapability()); err != nil {
		t.Fatal(err)
	}
	for _, conn := range []*websocket.Conn{sender, receiver} {
		_ = conn.SetReadDeadline(time.Now().Add(time.Second))
		var response negotiationResponse
		if err := conn.ReadJSON(&response); err != nil {
			t.Fatal(err)
		}
		if response.Type != "negotiated" || response.Route != "webrtc" || response.Pair != "web-web" {
			t.Fatalf("response = %#v", response)
		}
		_, _, err := conn.ReadMessage()
		var closeErr *websocket.CloseError
		if !errors.As(err, &closeErr) || closeErr.Code != websocket.CloseNormalClosure {
			t.Fatalf("close error = %v", err)
		}
	}
}
