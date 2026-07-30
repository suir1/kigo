package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/suir1/kigo/internal/directcandidate"
	"github.com/suir1/kigo/internal/netprobe"
	"github.com/suir1/kigo/internal/relay"
)

func TestDirectRendezvousURL(t *testing.T) {
	got, err := rendezvousURL(
		"https://kigo.example/base?ignored=1#fragment",
		"/api/direct/",
		appNegotiationTestToken,
		"receiver",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "wss://kigo.example/base/api/direct/" + appNegotiationTestToken + "?role=receiver"
	if got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
}

func TestExchangeDirectCapability(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/direct/"+appNegotiationTestToken ||
			r.URL.Query().Get("role") != "sender" {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var capability directRendezvousCapability
		if err := conn.ReadJSON(&capability); err != nil {
			return
		}
		if capability.Type != "direct" ||
			capability.Version != directRendezvousVersion ||
			capability.ConnectionCount != 4 ||
			capability.Preference != relay.DirectPreferencePrefer ||
			!capability.Bidirectional ||
			!capability.RelayFallback ||
			!capability.NATProbe ||
			capability.NATClass != "cone" ||
			!capability.UDPPunch ||
			len(capability.UDPCandidates) != 1 ||
			len(capability.Candidates) != 1 ||
			len(capability.CandidateMetadata) != 1 ||
			capability.CandidateMetadata[0].Kind != directcandidate.KindManual {
			_ = conn.WriteJSON(map[string]string{"type": "error", "error": "unexpected capability"})
			return
		}
		_ = conn.WriteJSON(directRendezvousResponse{
			Type:           "direct_peer",
			Version:        directRendezvousVersion,
			PeerCandidates: []string{"[2001:db8::2]:4000"},
			PeerCandidateMetadata: []directcandidate.Candidate{{
				Address:  "[2001:db8::2]:4000",
				Kind:     directcandidate.KindIPv6Global,
				Priority: directcandidate.PriorityIPv6Global,
			}},
			PeerConnectionCount: 2,
			PeerPreference:      relay.DirectPreferencePrefer,
			PeerBidirectional:   true,
			PunchAtMillis:       time.Now().Add(150 * time.Millisecond).UnixMilli(),
			PeerRelayFallback:   true,
			PeerNATProbe:        true,
			PeerNATClass:        "symmetric",
			PeerUDPPunch:        true,
			PeerUDPCandidates:   []string{"[2001:db8::2]:5000"},
		})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	result, err := exchangeDirectCapability(
		ctx,
		&globalOptions{
			Signal:      server.URL,
			Relay:       "relay.example:9000",
			Connections: 4,
			UDPProbe:    true,
		},
		appNegotiationTestToken,
		"sender",
		[]string{"127.0.0.1:4000"},
		[]directcandidate.Candidate{{
			Address:  "127.0.0.1:4000",
			Kind:     directcandidate.KindManual,
			Priority: directcandidate.PriorityManual,
		}},
		true,
		netprobe.NATCone,
		[]string{"127.0.0.1:5000"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.PeerConnectionCount != 2 ||
		result.PeerPreference != relay.DirectPreferencePrefer ||
		!result.PeerBidirectional ||
		result.PunchAtMillis <= 0 ||
		!result.PeerRelayFallback ||
		!result.PeerNATProbe ||
		result.PeerNATClass != "symmetric" ||
		!result.PeerUDPPunch ||
		len(result.PeerUDPCandidates) != 1 ||
		len(result.PeerCandidateMetadata) != 1 ||
		result.PeerCandidateMetadata[0].Kind != directcandidate.KindIPv6Global {
		t.Fatalf("response = %#v", result)
	}
}
