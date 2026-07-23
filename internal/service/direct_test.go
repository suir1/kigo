package service

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/suir1/kigo/internal/directcandidate"
)

func validDirectCapability() directCapability {
	return directCapability{
		Type:       "direct",
		Version:    directRendezvousVersion,
		Candidates: []string{"127.0.0.1:4000", "[::1]:4000"},
		CandidateMetadata: []directcandidate.Candidate{{
			Address:  "127.0.0.1:4000",
			Kind:     directcandidate.KindLoopback,
			Priority: directcandidate.PriorityLoopback,
		}},
		ConnectionCount: 4,
		Preference:      "prefer-direct",
		Bidirectional:   true,
		RelayFallback:   true,
		NATProbe:        true,
		NATClass:        "cone",
		UDPPunch:        true,
		UDPCandidates:   []string{"127.0.0.1:5000"},
	}
}

func TestValidateDirectCapability(t *testing.T) {
	if err := validateDirectCapability(validDirectCapability()); err != nil {
		t.Fatal(err)
	}
	legacy := validDirectCapability()
	legacy.NATProbe = false
	legacy.NATClass = ""
	legacy.UDPPunch = false
	legacy.UDPCandidates = nil
	legacy.CandidateMetadata = nil
	legacy.Bidirectional = false
	if err := validateDirectCapability(legacy); err != nil {
		t.Fatalf("legacy capability: %v", err)
	}
	tests := []directCapability{
		{Type: "bad", Version: directRendezvousVersion, ConnectionCount: 1, Preference: "prefer-direct"},
		{Type: "direct", Version: 2, ConnectionCount: 1, Preference: "prefer-direct"},
		{Type: "direct", Version: directRendezvousVersion, ConnectionCount: 0, Preference: "prefer-direct"},
		{Type: "direct", Version: directRendezvousVersion, ConnectionCount: 9, Preference: "prefer-direct"},
		{Type: "direct", Version: directRendezvousVersion, ConnectionCount: 1, Preference: "bad"},
		{
			Type:            "direct",
			Version:         directRendezvousVersion,
			ConnectionCount: 1,
			Preference:      "prefer-direct",
			NATClass:        "bad",
		},
		{
			Type:            "direct",
			Version:         directRendezvousVersion,
			ConnectionCount: 1,
			Preference:      "prefer-direct",
			NATClass:        "cone",
		},
		{
			Type:            "direct",
			Version:         directRendezvousVersion,
			ConnectionCount: 1,
			Preference:      "prefer-direct",
			NATProbe:        true,
		},
		{
			Type:            "direct",
			Version:         directRendezvousVersion,
			ConnectionCount: 1,
			Preference:      "prefer-direct",
			Bidirectional:   true,
		},
		{
			Type:            "direct",
			Version:         directRendezvousVersion,
			ConnectionCount: 1,
			Preference:      "prefer-direct",
			UDPPunch:        true,
		},
		{
			Type:            "direct",
			Version:         directRendezvousVersion,
			ConnectionCount: 1,
			Preference:      "prefer-direct",
			UDPPunch:        true,
			UDPCandidates:   []string{"127.0.0.1:5000"},
		},
		{
			Type:            "direct",
			Version:         directRendezvousVersion,
			ConnectionCount: 1,
			Preference:      "prefer-direct",
			UDPCandidates:   []string{"127.0.0.1:5000"},
		},
		{
			Type:            "direct",
			Version:         directRendezvousVersion,
			ConnectionCount: 1,
			Preference:      "prefer-direct",
			Candidates:      []string{"bad"},
		},
		{
			Type:            "direct",
			Version:         directRendezvousVersion,
			ConnectionCount: 1,
			Preference:      "prefer-direct",
			Candidates:      []string{"127.0.0.1:4000"},
			CandidateMetadata: []directcandidate.Candidate{{
				Address:  "127.0.0.1:4000",
				Kind:     "bad",
				Priority: 50,
			}},
		},
		{
			Type:            "direct",
			Version:         directRendezvousVersion,
			ConnectionCount: 1,
			Preference:      "prefer-direct",
			Candidates:      []string{"127.0.0.1:4000"},
			CandidateMetadata: []directcandidate.Candidate{{
				Address:  "127.0.0.1:4000",
				Kind:     directcandidate.KindLoopback,
				Priority: 101,
			}},
		},
		{
			Type:            "direct",
			Version:         directRendezvousVersion,
			ConnectionCount: 1,
			Preference:      "prefer-direct",
			Candidates:      []string{"127.0.0.1:4000"},
			CandidateMetadata: []directcandidate.Candidate{{
				Address:  "127.0.0.1:4001",
				Kind:     directcandidate.KindLoopback,
				Priority: directcandidate.PriorityLoopback,
			}},
		},
	}
	tooMany := validDirectCapability()
	tooMany.Candidates = make([]string, maxDirectCandidates+1)
	for index := range tooMany.Candidates {
		tooMany.Candidates[index] = "127.0.0.1:4000"
	}
	tests = append(tests, tooMany)
	for _, capability := range tests {
		if err := validateDirectCapability(capability); err == nil {
			t.Fatalf("invalid capability was accepted: %#v", capability)
		}
	}
}

func TestDirectRendezvousExchangesPeerCapabilities(t *testing.T) {
	s := New(Config{RoomTTL: time.Minute})
	senderCapability := validDirectCapability()
	sender, err := s.joinDirect(negotiationTestToken, "sender", senderCapability)
	if err != nil {
		t.Fatal(err)
	}
	receiverCapability := validDirectCapability()
	receiverCapability.Candidates = nil
	receiverCapability.CandidateMetadata = nil
	receiverCapability.Bidirectional = false
	receiverCapability.ConnectionCount = 2
	receiverCapability.Preference = "prefer-relay"
	receiverCapability.RelayFallback = false
	receiverCapability.NATClass = "symmetric"
	receiverCapability.UDPPunch = false
	receiverCapability.UDPCandidates = nil
	receiver, err := s.joinDirect(negotiationTestToken, "receiver", receiverCapability)
	if err != nil {
		t.Fatal(err)
	}

	select {
	case outcome := <-sender.result:
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if outcome.response.PeerConnectionCount != 2 ||
			outcome.response.PeerPreference != "prefer-relay" ||
			outcome.response.PeerRelayFallback ||
			!outcome.response.PeerNATProbe ||
			outcome.response.PeerNATClass != "symmetric" ||
			outcome.response.PeerUDPPunch ||
			len(outcome.response.PeerUDPCandidates) != 0 ||
			outcome.response.PeerBidirectional ||
			outcome.response.PunchAtMillis != 0 ||
			len(outcome.response.PeerCandidates) != 0 {
			t.Fatalf("sender response = %#v", outcome.response)
		}
	case <-time.After(time.Second):
		t.Fatal("sender did not receive peer capability")
	}
	select {
	case outcome := <-receiver.result:
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if outcome.response.PeerConnectionCount != 4 ||
			outcome.response.PeerPreference != "prefer-direct" ||
			!outcome.response.PeerRelayFallback ||
			!outcome.response.PeerNATProbe ||
			outcome.response.PeerNATClass != "cone" ||
			!outcome.response.PeerUDPPunch ||
			len(outcome.response.PeerUDPCandidates) != 1 ||
			!outcome.response.PeerBidirectional ||
			outcome.response.PunchAtMillis != 0 ||
			len(outcome.response.PeerCandidateMetadata) != 1 ||
			len(outcome.response.PeerCandidates) != 2 {
			t.Fatalf("receiver response = %#v", outcome.response)
		}
	case <-time.After(time.Second):
		t.Fatal("receiver did not receive peer capability")
	}
}

func TestDirectWebSocketExchangesPeerCapabilities(t *testing.T) {
	s := New(Config{RoomTTL: time.Minute})
	server := httptest.NewServer(s.handler())
	defer server.Close()

	baseURL := "ws" + strings.TrimPrefix(server.URL, "http") +
		"/api/direct/" + negotiationTestToken + "?role="
	sender, _, err := websocket.DefaultDialer.Dial(baseURL+"sender", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	receiver, _, err := websocket.DefaultDialer.Dial(baseURL+"receiver", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()

	senderCapability := validDirectCapability()
	receiverCapability := validDirectCapability()
	receiverCapability.Candidates = []string{"192.0.2.20:4100"}
	receiverCapability.CandidateMetadata = []directcandidate.Candidate{{
		Address:  "192.0.2.20:4100",
		Kind:     directcandidate.KindUnknown,
		Priority: directcandidate.PriorityUnknown,
	}}
	receiverCapability.ConnectionCount = 2
	receiverCapability.Preference = "prefer-relay"
	receiverCapability.RelayFallback = false
	receiverCapability.NATClass = "symmetric"
	receiverCapability.UDPCandidates = []string{"192.0.2.20:5100"}
	if err := sender.WriteJSON(senderCapability); err != nil {
		t.Fatal(err)
	}
	if err := receiver.WriteJSON(receiverCapability); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(time.Second)
	if err := sender.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := receiver.SetReadDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	var senderResponse directResponse
	if err := sender.ReadJSON(&senderResponse); err != nil {
		t.Fatal(err)
	}
	if senderResponse.Type != "direct_peer" ||
		senderResponse.PeerConnectionCount != 2 ||
		senderResponse.PeerPreference != "prefer-relay" ||
		!senderResponse.PeerBidirectional ||
		senderResponse.PunchAtMillis <= 0 ||
		senderResponse.PeerRelayFallback ||
		!senderResponse.PeerNATProbe ||
		senderResponse.PeerNATClass != "symmetric" ||
		!senderResponse.PeerUDPPunch ||
		len(senderResponse.PeerUDPCandidates) != 1 ||
		len(senderResponse.PeerCandidateMetadata) != 1 ||
		len(senderResponse.PeerCandidates) != 1 {
		t.Fatalf("sender response = %#v", senderResponse)
	}
	var receiverResponse directResponse
	if err := receiver.ReadJSON(&receiverResponse); err != nil {
		t.Fatal(err)
	}
	if receiverResponse.Type != "direct_peer" ||
		receiverResponse.PeerConnectionCount != 4 ||
		receiverResponse.PeerPreference != "prefer-direct" ||
		!receiverResponse.PeerBidirectional ||
		receiverResponse.PunchAtMillis != senderResponse.PunchAtMillis ||
		!receiverResponse.PeerRelayFallback ||
		!receiverResponse.PeerNATProbe ||
		receiverResponse.PeerNATClass != "cone" ||
		!receiverResponse.PeerUDPPunch ||
		len(receiverResponse.PeerUDPCandidates) != 1 ||
		len(receiverResponse.PeerCandidateMetadata) != 1 ||
		len(receiverResponse.PeerCandidates) != 2 {
		t.Fatalf("receiver response = %#v", receiverResponse)
	}
}

func TestDirectRendezvousRejectsDuplicateRole(t *testing.T) {
	s := New(Config{RoomTTL: time.Minute})
	first, err := s.joinDirect(negotiationTestToken, "sender", validDirectCapability())
	if err != nil {
		t.Fatal(err)
	}
	defer s.leaveDirect(negotiationTestToken, first)
	if _, err := s.joinDirect(
		negotiationTestToken,
		"sender",
		validDirectCapability(),
	); err == nil || !strings.Contains(err.Error(), "already waiting") {
		t.Fatalf("duplicate error = %v", err)
	}
}

func TestCleanupExpiresDirectRendezvous(t *testing.T) {
	s := New(Config{RoomTTL: time.Minute})
	peer, err := s.joinDirect(negotiationTestToken, "sender", validDirectCapability())
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.directs.rooms[negotiationTestToken].expires = time.Now().Add(-time.Second)
	s.mu.Unlock()

	s.cleanup()

	select {
	case outcome := <-peer.result:
		if outcome.err == nil || !strings.Contains(outcome.err.Error(), "expired") {
			t.Fatalf("outcome = %#v", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("expired direct room did not notify waiter")
	}
}
