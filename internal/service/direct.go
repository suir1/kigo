package service

import (
	"errors"
	"net/http"
	"time"

	"github.com/suir1/kigo/internal/directcandidate"
	"github.com/suir1/kigo/internal/netprobe"
)

const (
	directRendezvousVersion  = 1
	maxDirectRendezvousBytes = 16 << 10
	maxDirectCandidates      = directcandidate.MaxCandidates
)

type directCapability struct {
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

type directResponse struct {
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
}

type directPeer = rendezvousPeer[directCapability, directResponse]

func newDirectRegistry() *rendezvousRegistry[directCapability, directResponse] {
	return newRendezvousRegistry[directCapability, directResponse]("direct rendezvous", 0)
}

func (s *Server) handleDirect(w http.ResponseWriter, r *http.Request) {
	token, role, conn, ok := s.openRendezvousWebSocket(
		w, r, "/api/direct/", "direct", maxDirectRendezvousBytes,
	)
	if !ok {
		return
	}
	defer closeRendezvousWebSocket(conn)
	var capability directCapability
	if err := conn.ReadJSON(&capability); err != nil {
		_ = conn.WriteJSON(map[string]string{"type": "error", "error": "direct capability required"})
		return
	}
	if err := validateDirectCapability(capability); err != nil {
		_ = conn.WriteJSON(map[string]string{"type": "error", "error": err.Error()})
		return
	}
	peer, err := s.joinDirect(token, role, capability)
	if err != nil {
		_ = conn.WriteJSON(map[string]string{"type": "error", "error": err.Error()})
		return
	}
	defer s.leaveDirect(token, peer)

	serveRendezvousResult(conn, peer.result)
}

func validateDirectCapability(capability directCapability) error {
	if capability.Type != "direct" || capability.Version != directRendezvousVersion {
		return errors.New("unsupported direct rendezvous protocol")
	}
	if capability.ConnectionCount < 1 || capability.ConnectionCount > 8 {
		return errors.New("direct connection count must be between 1 and 8")
	}
	if capability.Preference != "prefer-direct" && capability.Preference != "prefer-relay" {
		return errors.New("invalid direct preference")
	}
	if capability.NATClass != "" && !netprobe.ValidNATClass(capability.NATClass) {
		return errors.New("invalid direct NAT class")
	}
	if capability.NATClass != "" && !capability.NATProbe {
		return errors.New("direct NAT class requires NAT probe capability")
	}
	if capability.NATProbe && capability.NATClass == "" {
		return errors.New("direct NAT probe capability requires NAT class")
	}
	if capability.UDPPunch && len(capability.UDPCandidates) == 0 {
		return errors.New("UDP punch capability requires candidates")
	}
	if capability.UDPPunch && !capability.Bidirectional {
		return errors.New("UDP punch capability requires bidirectional direct")
	}
	if !capability.UDPPunch && len(capability.UDPCandidates) > 0 {
		return errors.New("UDP punch candidates require capability")
	}
	if err := directcandidate.ValidateSet(capability.UDPCandidates, nil); err != nil {
		return err
	}
	if capability.Bidirectional && len(capability.Candidates) == 0 {
		return errors.New("bidirectional direct requires candidates")
	}
	if err := directcandidate.ValidateSet(capability.Candidates, capability.CandidateMetadata); err != nil {
		return err
	}
	return nil
}

func (s *Server) joinDirect(token, role string, capability directCapability) (*directPeer, error) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	joined, room, _, err := s.directs.join(token, role, capability, false, now, s.cfg.RoomTTL)
	if err != nil {
		return nil, err
	}
	s.completeDirectLocked(token, room)
	return joined, nil
}

func (s *Server) completeDirectLocked(token string, room *rendezvousRoom[directCapability, directResponse]) {
	sender := room.slots["sender"]
	receiver := room.slots["receiver"]
	if sender == nil || receiver == nil {
		return
	}
	punchAtMillis := int64(0)
	if sender.cap.Bidirectional && receiver.cap.Bidirectional {
		punchAtMillis = time.Now().Add(150 * time.Millisecond).UnixMilli()
	}
	sender.result <- rendezvousOutcome[directResponse]{response: directPeerResponse(receiver.cap, punchAtMillis)}
	receiver.result <- rendezvousOutcome[directResponse]{response: directPeerResponse(sender.cap, punchAtMillis)}
	s.directs.complete(token)
}

func directPeerResponse(peer directCapability, punchAtMillis int64) directResponse {
	return directResponse{
		Type:                  "direct_peer",
		Version:               directRendezvousVersion,
		PeerCandidates:        append([]string(nil), peer.Candidates...),
		PeerCandidateMetadata: append([]directcandidate.Candidate(nil), peer.CandidateMetadata...),
		PeerConnectionCount:   peer.ConnectionCount,
		PeerPreference:        peer.Preference,
		PeerBidirectional:     peer.Bidirectional,
		PunchAtMillis:         punchAtMillis,
		PeerRelayFallback:     peer.RelayFallback,
		PeerNATProbe:          peer.NATProbe,
		PeerNATClass:          peer.NATClass,
		PeerUDPPunch:          peer.UDPPunch,
		PeerUDPCandidates:     append([]string(nil), peer.UDPCandidates...),
	}
}

func (s *Server) leaveDirect(token string, peer *directPeer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if peer != nil {
		s.directs.leave(token, peer.role, peer)
	}
}
