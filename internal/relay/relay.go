package relay

import (
	"context"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/suir1/kigo/internal/directcandidate"
	"github.com/suir1/kigo/internal/discovery"
	"github.com/suir1/kigo/internal/netreuse"
	"github.com/suir1/kigo/internal/transport"
)

type joinMessage struct {
	Type                    string                      `json:"type"`
	RoomToken               string                      `json:"room_token"`
	Role                    string                      `json:"role,omitempty"`
	ConnectionIndex         int                         `json:"connection_index,omitempty"`
	ConnectionCount         int                         `json:"connection_count,omitempty"`
	Pass                    string                      `json:"pass,omitempty"`
	Direct                  string                      `json:"direct,omitempty"`
	DirectCandidates        []string                    `json:"direct_candidates,omitempty"`
	DirectCandidateMetadata []directcandidate.Candidate `json:"direct_candidate_meta,omitempty"`
	UDPCandidates           []string                    `json:"udp_candidates,omitempty"`
	Capabilities            []string                    `json:"capabilities,omitempty"`
	DirectPreference        string                      `json:"direct_preference,omitempty"`
}

type statusMessage struct {
	Type                        string                      `json:"type"`
	Error                       string                      `json:"error,omitempty"`
	PublicAddress               string                      `json:"public_address,omitempty"`
	PunchAtMillis               int64                       `json:"punch_at_ms,omitempty"`
	PeerConnectionCount         int                         `json:"peer_connection_count,omitempty"`
	PeerDirect                  string                      `json:"peer_direct,omitempty"`
	PeerDirectCandidates        []string                    `json:"peer_direct_candidates,omitempty"`
	PeerDirectCandidateMetadata []directcandidate.Candidate `json:"peer_direct_candidate_meta,omitempty"`
	PeerUDPCandidates           []string                    `json:"peer_udp_candidates,omitempty"`
	PeerCapabilities            []string                    `json:"peer_capabilities,omitempty"`
	PeerDirectPreference        string                      `json:"peer_direct_preference,omitempty"`
}

type client struct {
	t                       *transport.TCPTransport
	conn                    net.Conn
	role                    string
	room                    string
	connectionIndex         int
	connectionCount         int
	direct                  string
	directCandidates        []string
	directCandidateMetadata []directcandidate.Candidate
	udpCandidates           []string
	capabilities            []string
	directPreference        string
}

type room struct {
	sender   *client
	receiver *client
	created  time.Time
}

type Server struct {
	mu          sync.Mutex
	waiting     map[string]*room
	waitTTL     time.Duration
	pass        string
	tokenSecret string
}

func NewServer() *Server {
	return &Server{waiting: map[string]*room{}, waitTTL: 10 * time.Minute}
}

func Run(ctx context.Context, listen string, waitTTL time.Duration, pass string) error {
	return RunWithOptions(ctx, RunOptions{
		Listen:  listen,
		WaitTTL: waitTTL,
		Pass:    pass,
	})
}

type RunOptions struct {
	Listen        string
	WaitTTL       time.Duration
	Pass          string
	TokenSecret   string
	LANAnnounce   bool
	DiscoveryAddr string
	Interface     string
}

func RunWithOptions(ctx context.Context, opts RunOptions) error {
	listen := opts.Listen
	if listen == "" {
		listen = ":9000"
	}
	waitTTL := opts.WaitTTL
	if waitTTL <= 0 {
		waitTTL = 10 * time.Minute
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	log.Printf("kigo relay listening on %s", ln.Addr())
	if opts.LANAnnounce {
		port, err := listenerPort(ln.Addr())
		if err != nil {
			_ = ln.Close()
			return err
		}
		go func() {
			if err := discovery.AnnounceOnInterface(ctx, opts.DiscoveryAddr, port, opts.Interface); err != nil && ctx.Err() == nil {
				log.Printf("relay LAN announcement failed: %v", err)
			}
		}()
		log.Printf("kigo relay announcing on %s", defaultDiscoveryAddr(opts.DiscoveryAddr))
	}
	server := NewServer()
	server.waitTTL = waitTTL
	server.pass = opts.Pass
	server.tokenSecret = opts.TokenSecret
	return server.Serve(ctx, ln)
}

func listenerPort(addr net.Addr) (int, error) {
	_, portText, err := net.SplitHostPort(addr.String())
	if err != nil {
		return 0, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port <= 0 {
		return 0, fmt.Errorf("invalid relay listen port %q", portText)
	}
	return port, nil
}

func defaultDiscoveryAddr(addr string) string {
	if addr == "" {
		return discovery.DefaultAddr
	}
	return addr
}

func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	go s.cleanupLoop(ctx)
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handle(ctx, conn)
	}
}

type JoinOptions struct {
	Addr                    string
	RoomToken               string
	Role                    string
	ConnectionIndex         int
	ConnectionCount         int
	Pass                    string
	Direct                  string
	DirectCandidates        []string
	DirectCandidateMetadata []directcandidate.Candidate
	UDPCandidates           []string
	DirectProbeLocalPort    int
	Capabilities            []string
	DirectPreference        string
	DialContext             DialContextFunc
}

type DialContextFunc func(context.Context, string, string) (net.Conn, error)

type JoinResult struct {
	Transport                   transport.Transport
	PunchAtMillis               int64
	PeerConnectionCount         int
	PeerDirect                  string
	PeerDirectCandidates        []string
	PeerDirectCandidateMetadata []directcandidate.Candidate
	PeerUDPCandidates           []string
	PeerCapabilities            []string
	PeerDirectPreference        string
	dialContext                 DialContextFunc
}

const (
	CapabilityRouteChoiceV1         = "route-choice-v1"
	CapabilityBidirectionalDirectV1 = "bidirectional-direct-v1"
	CapabilityUDPPunchV1            = "udp-punch-v1"
	CapabilityLANUpgradeV1          = "lan-upgrade-v1"
	DirectPreferencePrefer          = "prefer-direct"
	DirectPreferenceRelay           = "prefer-relay"
)

func Join(ctx context.Context, addr, roomToken, role, pass string) (transport.Transport, error) {
	result, err := JoinWithOptions(ctx, JoinOptions{Addr: addr, RoomToken: roomToken, Role: role, Pass: pass})
	if err != nil {
		return nil, err
	}
	return result.Transport, nil
}

func JoinWithOptions(ctx context.Context, opts JoinOptions) (JoinResult, error) {
	dialContext := opts.DialContext
	if dialContext == nil {
		var d net.Dialer
		dialContext = d.DialContext
	}
	conn, err := dialContext(ctx, "tcp", opts.Addr)
	if err != nil {
		return JoinResult{}, err
	}
	stopContextWatch := transport.CloseOnContextDone(ctx, conn)
	defer stopContextWatch()
	t := transport.NewTCPTransport(conn)
	if err := sendJSON(ctx, t, joinMessage{
		Type:                    "join",
		RoomToken:               opts.RoomToken,
		Role:                    opts.Role,
		ConnectionIndex:         opts.ConnectionIndex,
		ConnectionCount:         opts.ConnectionCount,
		Pass:                    opts.Pass,
		Direct:                  opts.Direct,
		DirectCandidates:        opts.DirectCandidates,
		DirectCandidateMetadata: opts.DirectCandidateMetadata,
		UDPCandidates:           opts.UDPCandidates,
		Capabilities:            opts.Capabilities,
		DirectPreference:        opts.DirectPreference,
	}); err != nil {
		_ = conn.Close()
		return JoinResult{}, err
	}
	var status statusMessage
	if err := recvJSON(ctx, t, &status); err != nil {
		_ = conn.Close()
		return JoinResult{}, err
	}
	if status.Type == "error" {
		_ = conn.Close()
		return JoinResult{}, errors.New(status.Error)
	}
	if status.Type != "ready" {
		_ = conn.Close()
		return JoinResult{}, fmt.Errorf("unexpected relay status %q", status.Type)
	}
	candidates := status.PeerDirectCandidates
	if len(candidates) == 0 && status.PeerDirect != "" {
		candidates = []string{status.PeerDirect}
	}
	return JoinResult{
		Transport:            t,
		PunchAtMillis:        status.PunchAtMillis,
		PeerConnectionCount:  normalizedRelayConnectionCount(status.PeerConnectionCount),
		PeerDirect:           status.PeerDirect,
		PeerDirectCandidates: candidates,
		PeerDirectCandidateMetadata: append(
			[]directcandidate.Candidate(nil),
			status.PeerDirectCandidateMetadata...,
		),
		PeerUDPCandidates:    append([]string(nil), status.PeerUDPCandidates...),
		PeerCapabilities:     status.PeerCapabilities,
		PeerDirectPreference: status.PeerDirectPreference,
		dialContext:          opts.DialContext,
	}, nil
}

func (s *Server) handle(ctx context.Context, conn net.Conn) {
	t := transport.NewTCPTransport(conn)
	var join joinMessage
	if err := recvJSON(ctx, t, &join); err != nil {
		_ = conn.Close()
		return
	}
	if join.Type == "punch_probe" {
		s.handlePublicProbe(ctx, conn, t, join)
		return
	}
	connectionCount := join.ConnectionCount
	if connectionCount == 0 {
		connectionCount = 1
	}
	if join.Type != "join" || join.RoomToken == "" || !validRole(join.Role) ||
		connectionCount < 1 || connectionCount > 8 ||
		join.ConnectionIndex < 0 || join.ConnectionIndex >= connectionCount {
		_ = sendJSON(ctx, t, statusMessage{Type: "error", Error: "invalid join"})
		_ = conn.Close()
		return
	}
	if !s.authorize(join.RoomToken, join.Pass) {
		_ = sendJSON(ctx, t, statusMessage{Type: "error", Error: "relay password required"})
		_ = conn.Close()
		return
	}
	candidates := normalizeDirectCandidates(join.Direct, join.DirectCandidates)
	candidateMetadata := normalizeDirectCandidateMetadata(candidates, join.DirectCandidateMetadata)
	capabilities := normalizeCapabilities(join.Capabilities)
	udpCandidates := normalizeDirectCandidates("", join.UDPCandidates)
	if !hasCapability(capabilities, CapabilityUDPPunchV1) {
		udpCandidates = nil
	}
	c := &client{
		t:                       t,
		conn:                    conn,
		role:                    join.Role,
		room:                    roomKey(join.RoomToken, join.ConnectionIndex),
		connectionIndex:         join.ConnectionIndex,
		connectionCount:         connectionCount,
		direct:                  firstCandidate(candidates),
		directCandidates:        candidates,
		directCandidateMetadata: candidateMetadata,
		udpCandidates:           udpCandidates,
		capabilities:            capabilities,
		directPreference:        normalizeDirectPreference(join.DirectPreference),
	}
	peer, err := s.takePeer(c)
	if err != nil {
		_ = sendJSON(ctx, c.t, statusMessage{Type: "error", Error: err.Error()})
		_ = conn.Close()
		return
	}
	if peer == nil {
		return
	}
	punchAtMillis := directPunchAtMillis(c, peer)
	_ = sendJSON(ctx, c.t, statusMessage{
		Type:                        "ready",
		PunchAtMillis:               punchAtMillis,
		PeerConnectionCount:         peer.connectionCount,
		PeerDirect:                  peer.direct,
		PeerDirectCandidates:        peer.directCandidates,
		PeerDirectCandidateMetadata: peer.directCandidateMetadata,
		PeerUDPCandidates:           peer.udpCandidates,
		PeerCapabilities:            peer.capabilities,
		PeerDirectPreference:        peer.directPreference,
	})
	_ = sendJSON(ctx, peer.t, statusMessage{
		Type:                        "ready",
		PunchAtMillis:               punchAtMillis,
		PeerConnectionCount:         c.connectionCount,
		PeerDirect:                  c.direct,
		PeerDirectCandidates:        c.directCandidates,
		PeerDirectCandidateMetadata: c.directCandidateMetadata,
		PeerUDPCandidates:           c.udpCandidates,
		PeerCapabilities:            c.capabilities,
		PeerDirectPreference:        c.directPreference,
	})
	go pipe(ctx, c, peer)
	go pipe(ctx, peer, c)
}

func directPunchAtMillis(left, right *client) int64 {
	if left == nil || right == nil ||
		left.connectionIndex != 0 || right.connectionIndex != 0 ||
		len(left.directCandidates) == 0 || len(right.directCandidates) == 0 ||
		!hasCapability(left.capabilities, CapabilityBidirectionalDirectV1) ||
		!hasCapability(right.capabilities, CapabilityBidirectionalDirectV1) {
		return 0
	}
	return time.Now().Add(150 * time.Millisecond).UnixMilli()
}

func (s *Server) handlePublicProbe(
	ctx context.Context,
	conn net.Conn,
	t *transport.TCPTransport,
	probe joinMessage,
) {
	defer conn.Close()
	if probe.RoomToken == "" || !validRole(probe.Role) {
		_ = sendJSON(ctx, t, statusMessage{Type: "error", Error: "invalid punch probe"})
		return
	}
	if !s.authorize(probe.RoomToken, probe.Pass) {
		_ = sendJSON(ctx, t, statusMessage{Type: "error", Error: "relay password required"})
		return
	}
	address := conn.RemoteAddr().String()
	if err := directcandidate.ValidateAddress(address); err != nil {
		_ = sendJSON(ctx, t, statusMessage{Type: "error", Error: "invalid observed public address"})
		return
	}
	_ = sendJSON(ctx, t, statusMessage{Type: "punch_observed", PublicAddress: address})
}

type PublicProbeOptions struct {
	Addr      string
	RoomToken string
	Role      string
	Pass      string
	LocalPort int
	Network   string
	SourceIP  net.IP
}

func ProbePublicAddress(ctx context.Context, opts PublicProbeOptions) (string, error) {
	if opts.Addr == "" || opts.RoomToken == "" || !validRole(opts.Role) {
		return "", errors.New("invalid public probe options")
	}
	if opts.LocalPort < 1 || opts.LocalPort > 65535 {
		return "", errors.New("public probe local port must be between 1 and 65535")
	}
	network := opts.Network
	if network == "" {
		network = "tcp"
	}
	dialer := netreuse.TCPDialerForIP(opts.LocalPort, opts.SourceIP)
	conn, err := dialer.DialContext(ctx, network, opts.Addr)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	t := transport.NewTCPTransport(conn)
	if err := sendJSON(ctx, t, joinMessage{
		Type:      "punch_probe",
		RoomToken: opts.RoomToken,
		Role:      opts.Role,
		Pass:      opts.Pass,
	}); err != nil {
		return "", err
	}
	var status statusMessage
	if err := recvJSON(ctx, t, &status); err != nil {
		return "", err
	}
	if status.Type == "error" {
		return "", errors.New(status.Error)
	}
	if status.Type != "punch_observed" {
		return "", fmt.Errorf("unexpected public probe status %q", status.Type)
	}
	if err := directcandidate.ValidateAddress(status.PublicAddress); err != nil {
		return "", err
	}
	return status.PublicAddress, nil
}

func (s *Server) authorize(roomToken, credential string) bool {
	if s.pass == "" && s.tokenSecret == "" {
		return true
	}
	if s.pass != "" && hmac.Equal([]byte(credential), []byte(s.pass)) {
		return true
	}
	return ValidateCredential(s.tokenSecret, roomToken, credential, time.Now())
}

func (s *Server) takePeer(c *client) (*client, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.waiting[c.room]
	if r == nil {
		r = &room{created: time.Now()}
		s.waiting[c.room] = r
	}
	switch c.role {
	case "sender":
		if r.receiver != nil {
			peer := r.receiver
			delete(s.waiting, c.room)
			return peer, nil
		}
		if r.sender != nil {
			return nil, errors.New("sender already waiting")
		}
		r.sender = c
	case "receiver":
		if r.sender != nil {
			peer := r.sender
			delete(s.waiting, c.room)
			return peer, nil
		}
		if r.receiver != nil {
			return nil, errors.New("receiver already waiting")
		}
		r.receiver = c
	}
	return nil, nil
}

func roomKey(token string, connectionIndex int) string {
	return token + ":" + strconv.Itoa(connectionIndex)
}

func normalizedRelayConnectionCount(count int) int {
	if count <= 0 {
		return 1
	}
	return count
}

func (s *Server) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.cleanupExpired()
		}
	}
}

func (s *Server) cleanupExpired() {
	s.mu.Lock()
	now := time.Now()
	var expired []*client
	for token, r := range s.waiting {
		if now.Sub(r.created) < s.waitTTL {
			continue
		}
		if r.sender != nil {
			expired = append(expired, r.sender)
		}
		if r.receiver != nil {
			expired = append(expired, r.receiver)
		}
		delete(s.waiting, token)
	}
	s.mu.Unlock()
	for _, c := range expired {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_ = sendJSON(ctx, c.t, statusMessage{Type: "error", Error: "relay room expired"})
		cancel()
		_ = c.conn.Close()
	}
}

func validRole(role string) bool {
	return role == "sender" || role == "receiver"
}

func normalizeDirectCandidates(legacy string, candidates []string) []string {
	seen := make(map[string]struct{}, len(candidates)+1)
	out := make([]string, 0, min(len(candidates)+1, directcandidate.MaxCandidates))
	add := func(candidate string) {
		if len(out) >= directcandidate.MaxCandidates || candidate == "" {
			return
		}
		if directcandidate.ValidateAddress(candidate) != nil {
			return
		}
		if _, ok := seen[candidate]; ok {
			return
		}
		seen[candidate] = struct{}{}
		out = append(out, candidate)
	}
	add(legacy)
	for _, candidate := range candidates {
		add(candidate)
	}
	return out
}

func normalizeDirectCandidateMetadata(
	candidates []string,
	metadata []directcandidate.Candidate,
) []directcandidate.Candidate {
	known := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		known[candidate] = struct{}{}
	}
	seen := make(map[string]struct{}, len(metadata))
	out := make([]directcandidate.Candidate, 0, min(len(metadata), directcandidate.MaxCandidates))
	for _, candidate := range metadata {
		if len(out) >= directcandidate.MaxCandidates || directcandidate.Validate(candidate) != nil {
			continue
		}
		if _, ok := known[candidate.Address]; !ok {
			continue
		}
		if _, ok := seen[candidate.Address]; ok {
			continue
		}
		seen[candidate.Address] = struct{}{}
		out = append(out, candidate)
	}
	return out
}

func firstCandidate(candidates []string) string {
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0]
}

func normalizeCapabilities(capabilities []string) []string {
	const maxCapabilities = 16
	const maxCapabilityLength = 64
	seen := make(map[string]struct{}, len(capabilities))
	out := make([]string, 0, min(len(capabilities), maxCapabilities))
	for _, capability := range capabilities {
		if capability == "" || len(capability) > maxCapabilityLength || len(out) >= maxCapabilities {
			continue
		}
		if _, ok := seen[capability]; ok {
			continue
		}
		seen[capability] = struct{}{}
		out = append(out, capability)
	}
	return out
}

func hasCapability(capabilities []string, target string) bool {
	for _, capability := range capabilities {
		if capability == target {
			return true
		}
	}
	return false
}

func normalizeDirectPreference(preference string) string {
	switch preference {
	case DirectPreferencePrefer, DirectPreferenceRelay:
		return preference
	default:
		return ""
	}
}

func pipe(ctx context.Context, from, to *client) {
	defer from.conn.Close()
	defer to.conn.Close()
	for {
		payload, err := from.t.Recv(ctx)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("relay recv failed: %v", err)
			}
			return
		}
		if err := to.t.Send(ctx, payload); err != nil {
			log.Printf("relay send failed: %v", err)
			return
		}
	}
}

func sendJSON(ctx context.Context, t transport.Transport, v any) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return t.Send(ctx, payload)
}

func recvJSON(ctx context.Context, t transport.Transport, v any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	payload, err := t.Recv(ctx)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, v)
}
