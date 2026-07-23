package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/suir1/kigo/internal/directcandidate"
	"github.com/suir1/kigo/internal/netprobe"
	"github.com/suir1/kigo/internal/relay"
)

const directRendezvousVersion = 1

type directRendezvousCapability struct {
	Type              string                      `json:"type"`
	Version           int                         `json:"version"`
	Candidates        []string                    `json:"candidates,omitempty"`
	CandidateMetadata []directcandidate.Candidate `json:"candidate_meta,omitempty"`
	ConnectionCount   int                         `json:"connection_count"`
	Preference        string                      `json:"preference"`
	Bidirectional     bool                        `json:"bidirectional,omitempty"`
	RelayFallback     bool                        `json:"relay_fallback,omitempty"`
	NATProbe          bool                        `json:"nat_probe,omitempty"`
	NATClass          string                      `json:"nat_class,omitempty"`
	UDPPunch          bool                        `json:"udp_punch,omitempty"`
	UDPCandidates     []string                    `json:"udp_candidates,omitempty"`
}

type directRendezvousResponse struct {
	Type                  string                      `json:"type"`
	Version               int                         `json:"version"`
	PeerCandidates        []string                    `json:"peer_candidates,omitempty"`
	PeerCandidateMetadata []directcandidate.Candidate `json:"peer_candidate_meta,omitempty"`
	PeerConnectionCount   int                         `json:"peer_connection_count"`
	PeerPreference        string                      `json:"peer_preference"`
	PeerBidirectional     bool                        `json:"peer_bidirectional,omitempty"`
	PunchAtMillis         int64                       `json:"punch_at_ms,omitempty"`
	PeerRelayFallback     bool                        `json:"peer_relay_fallback,omitempty"`
	PeerNATProbe          bool                        `json:"peer_nat_probe,omitempty"`
	PeerNATClass          string                      `json:"peer_nat_class,omitempty"`
	PeerUDPPunch          bool                        `json:"peer_udp_punch,omitempty"`
	PeerUDPCandidates     []string                    `json:"peer_udp_candidates,omitempty"`
	Error                 string                      `json:"error,omitempty"`
}

func exchangeDirectCapability(
	ctx context.Context,
	g *globalOptions,
	roomToken string,
	role string,
	candidates []string,
	candidateMetadata []directcandidate.Candidate,
	bidirectional bool,
	natClass netprobe.NATClass,
	udpCandidates []string,
) (directRendezvousResponse, error) {
	if !isSignalRoleClient(role) {
		return directRendezvousResponse{}, errors.New("invalid direct rendezvous role")
	}
	endpoint, err := directRendezvousURL(g.Signal, roomToken, role)
	if err != nil {
		return directRendezvousResponse{}, err
	}
	conn, response, err := outboundWebSocketDialer(g).DialContext(ctx, endpoint, nil)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		return directRendezvousResponse{}, fmt.Errorf("connect direct rendezvous: %w", err)
	}
	defer conn.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()
	capability := directRendezvousCapability{
		Type:              "direct",
		Version:           directRendezvousVersion,
		Candidates:        append([]string(nil), candidates...),
		CandidateMetadata: append([]directcandidate.Candidate(nil), candidateMetadata...),
		ConnectionCount:   g.Connections,
		Preference:        localDirectPreference(g),
		Bidirectional:     bidirectional,
		RelayFallback:     g.Relay != "",
		NATProbe:          g.UDPProbe,
	}
	if g.UDPProbe {
		capability.NATClass = string(natClass)
	}
	if len(udpCandidates) > 0 {
		capability.UDPPunch = true
		capability.UDPCandidates = append([]string(nil), udpCandidates...)
	}
	if err := conn.WriteJSON(capability); err != nil {
		return directRendezvousResponse{}, fmt.Errorf("send direct capability: %w", err)
	}
	var result directRendezvousResponse
	if err := conn.ReadJSON(&result); err != nil {
		return directRendezvousResponse{}, fmt.Errorf("read direct rendezvous: %w", err)
	}
	if result.Type == "error" {
		if result.Error == "" {
			result.Error = "direct rendezvous failed"
		}
		return directRendezvousResponse{}, errors.New(result.Error)
	}
	if result.Type != "direct_peer" || result.Version != directRendezvousVersion {
		return directRendezvousResponse{}, errors.New("unsupported direct rendezvous response")
	}
	if result.PeerConnectionCount < 1 || result.PeerConnectionCount > 8 {
		return directRendezvousResponse{}, errors.New("invalid peer direct connection count")
	}
	if result.PeerPreference != relay.DirectPreferencePrefer &&
		result.PeerPreference != relay.DirectPreferenceRelay {
		return directRendezvousResponse{}, errors.New("invalid peer direct preference")
	}
	if result.PeerNATClass != "" && !netprobe.ValidNATClass(result.PeerNATClass) {
		return directRendezvousResponse{}, errors.New("invalid peer direct NAT class")
	}
	if result.PeerNATClass != "" && !result.PeerNATProbe {
		return directRendezvousResponse{}, errors.New("peer direct NAT class requires NAT probe capability")
	}
	if result.PeerNATProbe && result.PeerNATClass == "" {
		return directRendezvousResponse{}, errors.New("peer direct NAT probe capability requires NAT class")
	}
	if result.PeerUDPPunch && len(result.PeerUDPCandidates) == 0 {
		return directRendezvousResponse{}, errors.New("peer UDP punch capability requires candidates")
	}
	if result.PeerUDPPunch && !result.PeerBidirectional {
		return directRendezvousResponse{}, errors.New("peer UDP punch capability requires bidirectional direct")
	}
	if !result.PeerUDPPunch && len(result.PeerUDPCandidates) > 0 {
		return directRendezvousResponse{}, errors.New("peer UDP punch candidates require capability")
	}
	if err := directcandidate.ValidateSet(result.PeerUDPCandidates, nil); err != nil {
		return directRendezvousResponse{}, fmt.Errorf("invalid peer UDP punch candidates: %w", err)
	}
	if result.PeerBidirectional && len(result.PeerCandidates) == 0 {
		return directRendezvousResponse{}, errors.New("peer bidirectional direct requires candidates")
	}
	if result.PeerBidirectional && result.PunchAtMillis <= 0 {
		return directRendezvousResponse{}, errors.New("peer bidirectional direct requires punch time")
	}
	if err := directcandidate.ValidateSet(result.PeerCandidates, result.PeerCandidateMetadata); err != nil {
		return directRendezvousResponse{}, fmt.Errorf("invalid peer direct candidates: %w", err)
	}
	return result, nil
}

func directRendezvousURL(base, roomToken, role string) (string, error) {
	httpURL, err := apiURL(base, "/api/direct/"+roomToken)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(httpURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("signaling URL must use http or https: %q", base)
	}
	query := u.Query()
	query.Set("role", role)
	u.RawQuery = query.Encode()
	return u.String(), nil
}
