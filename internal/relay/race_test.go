package relay

import (
	"context"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suir1/kigo/internal/directcandidate"
	"github.com/suir1/kigo/internal/netreuse"
)

func TestRaceJoinPrefersLANRelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lanAddr := startTestRelay(t, ctx)
	externalAddr := startTestRelay(t, ctx)
	candidates := []Candidate{
		{Addr: lanAddr, Kind: "lan", Priority: 0},
		{Addr: externalAddr, Kind: "external", Priority: 1, StartDelay: 250 * time.Millisecond},
	}

	senderCh := make(chan candidateResult, 1)
	go func() {
		result, err := RaceJoin(ctx, RaceOptions{
			Candidates: candidates,
			Join: JoinOptions{
				RoomToken: "race-room",
				Role:      "sender",
			},
			SettleWindow: 50 * time.Millisecond,
		})
		senderCh <- candidateResult{result: result, err: err}
	}()
	receiver, err := RaceJoin(ctx, RaceOptions{
		Candidates: candidates,
		Join: JoinOptions{
			RoomToken: "race-room",
			Role:      "receiver",
		},
		SettleWindow: 50 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Transport.Close()
	sender := <-senderCh
	if sender.err != nil {
		t.Fatal(sender.err)
	}
	defer sender.result.Transport.Close()
	if sender.result.Candidate.Kind != "lan" || receiver.Candidate.Kind != "lan" {
		t.Fatalf("sender=%#v receiver=%#v", sender.result.Candidate, receiver.Candidate)
	}
}

func TestRaceJoinFallsBackToExternalRelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	unreachable := closedRelayAddress(t)
	externalAddr := startTestRelay(t, ctx)
	candidates := []Candidate{
		{Addr: unreachable, Kind: "lan", Priority: 0},
		{Addr: externalAddr, Kind: "external", Priority: 1},
	}

	senderCh := make(chan candidateResult, 1)
	go func() {
		result, err := RaceJoin(ctx, RaceOptions{
			Candidates:   candidates,
			Join:         JoinOptions{RoomToken: "fallback-room", Role: "sender"},
			SettleWindow: 20 * time.Millisecond,
		})
		senderCh <- candidateResult{result: result, err: err}
	}()
	receiver, err := RaceJoin(ctx, RaceOptions{
		Candidates:   candidates,
		Join:         JoinOptions{RoomToken: "fallback-room", Role: "receiver"},
		SettleWindow: 20 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Transport.Close()
	sender := <-senderCh
	if sender.err != nil {
		t.Fatal(sender.err)
	}
	defer sender.result.Transport.Close()
	if sender.result.Candidate.Kind != "external" || receiver.Candidate.Kind != "external" {
		t.Fatalf("sender=%#v receiver=%#v", sender.result.Candidate, receiver.Candidate)
	}
}

func TestRaceJoinBypassesProxyForLANRelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	lanAddr := startTestRelay(t, ctx)
	externalAddr := startTestRelay(t, ctx)
	var proxyDials atomic.Int32
	dialContext := func(ctx context.Context, network, address string) (net.Conn, error) {
		proxyDials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	candidates := []Candidate{
		{Addr: lanAddr, Kind: "lan", Priority: 0},
		{Addr: externalAddr, Kind: "external", Priority: 1, StartDelay: 250 * time.Millisecond, UseProxy: true},
	}

	senderCh := make(chan candidateResult, 1)
	go func() {
		result, err := RaceJoin(ctx, RaceOptions{
			Candidates:   candidates,
			Join:         JoinOptions{RoomToken: "proxy-lan-room", Role: "sender", DialContext: dialContext},
			SettleWindow: 40 * time.Millisecond,
		})
		senderCh <- candidateResult{result: result, err: err}
	}()
	receiver, err := RaceJoin(ctx, RaceOptions{
		Candidates:   candidates,
		Join:         JoinOptions{RoomToken: "proxy-lan-room", Role: "receiver", DialContext: dialContext},
		SettleWindow: 40 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Transport.Close()
	sender := <-senderCh
	if sender.err != nil {
		t.Fatal(sender.err)
	}
	defer sender.result.Transport.Close()
	if proxyDials.Load() != 0 {
		t.Fatalf("proxy dial count = %d", proxyDials.Load())
	}
}

func TestRaceJoinUsesProxyForExternalRelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	externalAddr := startTestRelay(t, ctx)
	var proxyDials atomic.Int32
	dialContext := func(ctx context.Context, network, address string) (net.Conn, error) {
		proxyDials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	candidates := []Candidate{
		{Addr: closedRelayAddress(t), Kind: "lan", Priority: 0},
		{Addr: externalAddr, Kind: "external", Priority: 1, UseProxy: true},
	}

	senderCh := make(chan candidateResult, 1)
	go func() {
		result, err := RaceJoin(ctx, RaceOptions{
			Candidates: candidates,
			Join:       JoinOptions{RoomToken: "proxy-external-room", Role: "sender", DialContext: dialContext},
		})
		senderCh <- candidateResult{result: result, err: err}
	}()
	receiver, err := RaceJoin(ctx, RaceOptions{
		Candidates: candidates,
		Join:       JoinOptions{RoomToken: "proxy-external-room", Role: "receiver", DialContext: dialContext},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Transport.Close()
	sender := <-senderCh
	if sender.err != nil {
		t.Fatal(sender.err)
	}
	defer sender.result.Transport.Close()
	if proxyDials.Load() != 2 {
		t.Fatalf("proxy dial count = %d, want 2", proxyDials.Load())
	}
}

func TestRaceJoinPublishesRelayObservedPublicCandidate(t *testing.T) {
	if !netreuse.Supported {
		t.Skip("same-port socket reuse is unsupported on this platform")
	}
	tests := []struct{ role, candidate string }{
		{"sender", "10.0.0.2:4000"},
		{"receiver", "10.0.0.3:4000"},
	}
	for _, test := range tests {
		t.Run(test.role, func(t *testing.T) {
			publisherRole := test.role
			peerRole := "receiver"
			if publisherRole == "receiver" {
				peerRole = "sender"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			relayAddr := startTestRelay(t, ctx)
			directListener, err := netreuse.ListenTCP(ctx, "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer directListener.Close()
			directPort, err := listenerPort(directListener.Addr())
			if err != nil {
				t.Fatal(err)
			}
			roomToken := publisherRole + "-public-race-room"
			candidates := []Candidate{{Addr: relayAddr, Kind: "external"}}
			publisherCh := make(chan candidateResult, 1)
			go func() {
				result, err := RaceJoin(ctx, RaceOptions{
					Candidates: candidates,
					Join: JoinOptions{
						RoomToken:            roomToken,
						Role:                 publisherRole,
						DirectCandidates:     []string{test.candidate},
						DirectProbeLocalPort: directPort,
					},
				})
				publisherCh <- candidateResult{result: result, err: err}
			}()
			peer, err := RaceJoin(ctx, RaceOptions{
				Candidates: candidates,
				Join:       JoinOptions{RoomToken: roomToken, Role: peerRole},
			})
			if err != nil {
				t.Fatal(err)
			}
			defer peer.Transport.Close()
			publisher := <-publisherCh
			if publisher.err != nil {
				t.Fatal(publisher.err)
			}
			defer publisher.result.Transport.Close()
			if len(peer.PeerDirectCandidates) != 2 {
				t.Fatalf("peer candidates = %#v", peer.PeerDirectCandidates)
			}
			if len(peer.PeerDirectCandidateMetadata) != 1 {
				t.Fatalf("peer candidate metadata = %#v", peer.PeerDirectCandidateMetadata)
			}
			public := peer.PeerDirectCandidateMetadata[0]
			if public.Kind != directcandidate.KindPublic || public.Priority != directcandidate.PriorityPublic {
				t.Fatalf("public candidate = %#v", public)
			}
			_, portText, err := net.SplitHostPort(public.Address)
			if err != nil {
				t.Fatal(err)
			}
			if portText != strconv.Itoa(directPort) {
				t.Fatalf("public candidate = %q, want port %d", public.Address, directPort)
			}
		})
	}
}

func startTestRelay(t *testing.T, ctx context.Context) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := NewServer().Serve(ctx, ln); err != nil {
			t.Errorf("relay serve failed: %v", err)
		}
	}()
	return ln.Addr().String()
}

func closedRelayAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}
