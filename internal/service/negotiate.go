package service

import (
	"fmt"
	"net/http"
	"time"

	"github.com/suir1/kigo/internal/relay"
	"github.com/suir1/kigo/internal/routing"
)

const (
	negotiationVersion       = routing.NegotiationVersion
	maxNegotiationBytes      = 16 << 10
	passiveNegotiationTTL    = 30 * time.Second
	negotiationWebSocketWait = 10 * time.Second
	negotiationTransfer      = routing.ProtocolTransfer
	negotiationNote          = routing.ProtocolNote
)

type negotiationCapability = routing.Capability
type negotiationResponse = routing.Response

type negotiationPeer = rendezvousPeer[negotiationCapability, negotiationResponse]

func newNegotiationRegistry() *rendezvousRegistry[negotiationCapability, negotiationResponse] {
	return newRendezvousRegistry[negotiationCapability, negotiationResponse]("negotiation", passiveNegotiationTTL)
}

func (s *Server) handleNegotiate(w http.ResponseWriter, r *http.Request) {
	token, role, conn, ok := s.openRendezvousWebSocket(
		w, r, "/api/negotiate/", "negotiation", maxNegotiationBytes,
	)
	if !ok {
		return
	}
	defer closeRendezvousWebSocket(conn)
	var capability negotiationCapability
	if err := conn.ReadJSON(&capability); err != nil {
		_ = conn.WriteJSON(map[string]string{"type": "error", "error": "negotiation capability required"})
		return
	}
	if err := validateNegotiationCapability(capability); err != nil {
		_ = conn.WriteJSON(map[string]string{"type": "error", "error": err.Error()})
		return
	}
	peer, err := s.joinNegotiation(token, role, capability, false)
	if err != nil {
		_ = conn.WriteJSON(map[string]string{"type": "error", "error": err.Error()})
		return
	}
	defer s.leaveNegotiation(token, peer)

	serveRendezvousResult(conn, peer.result)
}

func validateNegotiationCapability(capability negotiationCapability) error {
	return routing.ValidateCapability(capability)
}

func (s *Server) joinNegotiation(
	token string,
	role string,
	capability negotiationCapability,
	passive bool,
) (*negotiationPeer, error) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	joined, room, changed, err := s.negotiations.join(token, role, capability, passive, now, s.cfg.RoomTTL)
	if err != nil {
		return nil, err
	}
	if !changed {
		return joined, nil
	}
	s.completeNegotiationLocked(token, room)
	return joined, nil
}

func (s *Server) observeWebRTCOnly(token, role string) {
	s.observeWebRTCOnlyProtocol(token, role, negotiationTransfer)
}

func (s *Server) observeWebRTCOnlyProtocol(token, role, protocol string) {
	capability := negotiationCapability{
		Type:     "negotiate",
		Version:  negotiationVersion,
		Client:   "webrtc",
		Protocol: protocol,
	}
	_, _ = s.joinNegotiation(token, role, capability, true)
}

func (s *Server) completeNegotiationLocked(
	token string,
	room *rendezvousRoom[negotiationCapability, negotiationResponse],
) {
	sender := room.slots["sender"]
	receiver := room.slots["receiver"]
	if sender == nil || receiver == nil {
		return
	}
	senderProtocol, _ := routing.NormalizeProtocol(sender.cap.Protocol)
	receiverProtocol, _ := routing.NormalizeProtocol(receiver.cap.Protocol)
	outcome := rendezvousOutcome[negotiationResponse]{}
	if senderProtocol != receiverProtocol {
		outcome.err = fmt.Errorf(
			"room protocol mismatch: sender uses %s, receiver uses %s",
			senderProtocol,
			receiverProtocol,
		)
	} else {
		outcome.response = routing.Choose(sender.cap, receiver.cap, s.cfg.NativeRelay)
	}
	if outcome.err == nil &&
		outcome.response.Reason == "service-native-relay" &&
		s.cfg.NativeRelaySecret != "" {
		credential, err := relay.IssueCredential(
			s.cfg.NativeRelaySecret,
			token,
			time.Now().Add(s.cfg.NativeRelayCredentialTTL),
		)
		if err != nil {
			outcome = rendezvousOutcome[negotiationResponse]{err: fmt.Errorf("issue native relay credential: %w", err)}
		} else {
			outcome.response.RelayCredential = credential
		}
	}
	for _, peer := range []*negotiationPeer{sender, receiver} {
		if peer.passive {
			continue
		}
		peer.result <- outcome
	}
	s.negotiations.complete(token)
}

func (s *Server) leaveNegotiation(token string, peer *negotiationPeer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if peer != nil {
		s.negotiations.leave(token, peer.role, peer)
	}
}
