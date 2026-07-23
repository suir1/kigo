package service

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/turn/v4"
	kigo "github.com/suir1/kigo"
	"github.com/suir1/kigo/internal/routing"
	"github.com/suir1/kigo/internal/version"
)

const defaultRoomTTL = 10 * time.Minute
const defaultNativeRelayCredentialTTL = 2 * time.Hour
const maxPendingSignals = 64
const signalSendQueueCapacity = maxPendingSignals + 16
const maxSignalMessageBytes = 1 << 20
const signalJoinTimeout = 10 * time.Second
const signalReconnectProtocol = "kigo-reconnect-v1"

const (
	websocketWriteWait  = 10 * time.Second
	websocketPongWait   = 60 * time.Second
	websocketPingPeriod = 54 * time.Second
)

type Config struct {
	Listen                    string
	WebDir                    string
	PublicURL                 string
	NativeRelay               string
	NativeRelaySecret         string
	NativeRelayCredentialTTL  time.Duration
	TURN                      string
	TURNListen                string
	TURNPublicIP              string
	TURNMinPort               int
	TURNMaxPort               int
	TURNUsername              string
	TURNCredential            string
	TURNSecret                string
	TURNRealm                 string
	TURNCredentialTTL         time.Duration
	TURNCredentialsPerMinute  int
	TURNMaxAllocations        int
	TURNMaxAllocationsPerUser int
	TURNMaxAllocationsPerIP   int
	TURNEgressWindow          time.Duration
	TURNMaxEgressBytes        int64
	TURNMaxEgressBytesPerUser int64
	TURNMaxEgressBytesPerIP   int64
	TLSCert                   string
	TLSKey                    string
	RoomTTL                   time.Duration
	SignalRequestsPerMinute   int
	TrustedProxies            string
}

type Server struct {
	cfg            Config
	started        time.Time
	rooms          map[string]*room
	negotiations   *rendezvousRegistry[negotiationCapability, negotiationResponse]
	directs        *rendezvousRegistry[directCapability, directResponse]
	signalLimits   map[string][]time.Time
	iceLimits      map[string][]time.Time
	turnServer     *turn.Server
	turnQuota      *turnAllocationQuota
	trustedProxies []netip.Prefix
	mu             sync.Mutex
	upgrader       websocket.Upgrader
}

type room struct {
	token              string
	protocol           string
	created            time.Time
	expires            time.Time
	locked             bool
	clients            map[*client]bool
	slots              map[string]*client
	reconnectTokens    map[string]string
	reconnectEnabled   bool
	generation         uint64
	needsNewGeneration bool
	pending            []json.RawMessage
}

type client struct {
	room         *room
	conn         *websocket.Conn
	send         chan []byte
	role         string
	explicitRole bool
	closed       bool
}

type signalJoin struct {
	Type           string `json:"type"`
	ReconnectToken string `json:"reconnect_token,omitempty"`
}

func New(cfg Config) *Server {
	if cfg.Listen == "" {
		cfg.Listen = ":9100"
	}
	if cfg.PublicURL == "" {
		cfg.PublicURL = defaultPublicURL(cfg.Listen, cfg.TLSCert != "" && cfg.TLSKey != "")
	}
	if cfg.RoomTTL <= 0 {
		cfg.RoomTTL = defaultRoomTTL
	}
	if cfg.SignalRequestsPerMinute == 0 {
		cfg.SignalRequestsPerMinute = 60
	}
	if cfg.NativeRelayCredentialTTL == 0 {
		cfg.NativeRelayCredentialTTL = defaultNativeRelayCredentialTTL
	}
	if cfg.TURNUsername == "" {
		cfg.TURNUsername = "kigo"
	}
	if cfg.TURNCredential == "" {
		cfg.TURNCredential = "kigo-turn"
	}
	if cfg.TURNRealm == "" {
		cfg.TURNRealm = "kigo"
	}
	if cfg.TURNCredentialTTL == 0 {
		cfg.TURNCredentialTTL = 2 * time.Hour
	}
	if cfg.TURNCredentialsPerMinute == 0 {
		cfg.TURNCredentialsPerMinute = 1200
	}
	if cfg.TURNMaxAllocations == 0 {
		cfg.TURNMaxAllocations = 1024
	}
	if cfg.TURNMaxAllocationsPerUser == 0 {
		cfg.TURNMaxAllocationsPerUser = 4
	}
	if cfg.TURNMaxAllocationsPerIP == 0 {
		cfg.TURNMaxAllocationsPerIP = 32
	}
	if cfg.TURNEgressWindow == 0 {
		cfg.TURNEgressWindow = time.Hour
	}
	if cfg.TURNMaxEgressBytes == 0 {
		cfg.TURNMaxEgressBytes = -1
	}
	if cfg.TURNMaxEgressBytesPerUser == 0 {
		cfg.TURNMaxEgressBytesPerUser = -1
	}
	if cfg.TURNMaxEgressBytesPerIP == 0 {
		cfg.TURNMaxEgressBytesPerIP = -1
	}
	trustedProxies, _ := parseTrustedProxies(cfg.TrustedProxies)
	server := &Server{
		cfg:            cfg,
		started:        time.Now(),
		rooms:          map[string]*room{},
		negotiations:   newNegotiationRegistry(),
		directs:        newDirectRegistry(),
		signalLimits:   map[string][]time.Time{},
		iceLimits:      map[string][]time.Time{},
		trustedProxies: trustedProxies,
	}
	server.upgrader = websocket.Upgrader{
		CheckOrigin:  server.checkOrigin,
		Subprotocols: []string{signalReconnectProtocol},
	}
	return server
}

func (s *Server) Run(ctx context.Context) error {
	if err := s.Validate(); err != nil {
		return err
	}
	if err := s.startTURN(ctx); err != nil {
		return err
	}
	server := s.newHTTPServer(ctx)
	ln, err := net.Listen("tcp", s.cfg.Listen)
	if err != nil {
		return err
	}
	scheme := "http"
	if s.usesTLS() {
		scheme = "https"
	}
	log.Printf("kigo service listening on %s://%s", scheme, ln.Addr())
	go s.cleanupLoop(ctx)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	if s.usesTLS() {
		err = server.ServeTLS(ln, s.cfg.TLSCert, s.cfg.TLSKey)
	} else {
		err = server.Serve(ln)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (s *Server) webFileSystem() http.FileSystem {
	if s.cfg.WebDir != "" {
		if info, err := os.Stat(s.cfg.WebDir); err == nil && info.IsDir() {
			return http.Dir(s.cfg.WebDir)
		}
		log.Printf("web directory %q not found; using embedded assets", s.cfg.WebDir)
	}
	return http.FS(kigo.EmbeddedWebFS())
}

func (s *Server) handleICE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.allowICERequest(r) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	stunURLs := []string{
		"stun:stun.l.google.com:19302",
		"stun:stun1.l.google.com:19302",
	}
	if stunURL := stunURLForTURN(s.cfg.TURN); stunURL != "" {
		stunURLs = append([]string{stunURL}, stunURLs...)
	}
	servers := []map[string]any{{"urls": stunURLs}}
	var credentialExpiresAt int64
	if s.cfg.TURN != "" {
		entry := map[string]any{"urls": []string{s.cfg.TURN}}
		username, credential, expiresAt, err := s.turnCredentials(time.Now())
		if err != nil {
			http.Error(w, "could not issue TURN credentials", http.StatusInternalServerError)
			return
		}
		if username != "" && credential != "" {
			entry["username"] = username
			entry["credential"] = credential
			credentialExpiresAt = expiresAt
		}
		servers = append(servers, entry)
	}
	if r.Method == http.MethodHead {
		return
	}
	body := map[string]any{"iceServers": servers}
	if credentialExpiresAt > 0 {
		body["credentialExpiresAt"] = credentialExpiresAt
	}
	_ = json.NewEncoder(w).Encode(body)
}

func stunURLForTURN(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	switch strings.ToLower(parsed.Scheme) {
	case "turn":
		parsed.Scheme = "stun"
	case "turns":
		parsed.Scheme = "stuns"
	default:
		return ""
	}
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	return parsed.String()
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodHead)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	stats := s.roomStats()
	turnStats := s.turnStats()
	body := map[string]any{
		"ok":           true,
		"version":      version.Get(),
		"capabilities": []string{"transport-negotiation-v1", "direct-rendezvous-v1", "signal-reconnect-v1"},
		"uptime_ms":    time.Since(s.started).Milliseconds(),
		"public_url":   s.cfg.PublicURL,
		"room_ttl_ms":  s.cfg.RoomTTL.Milliseconds(),
		"rooms":        stats,
		"native_relay": map[string]any{
			"configured":        s.cfg.NativeRelay != "",
			"endpoint":          s.cfg.NativeRelay,
			"credential_mode":   s.nativeRelayCredentialMode(),
			"credential_ttl_ms": s.cfg.NativeRelayCredentialTTL.Milliseconds(),
		},
		"turn": map[string]any{
			"configured":                 s.cfg.TURN != "",
			"built_in":                   s.cfg.TURNListen != "",
			"url":                        s.cfg.TURN,
			"credential_mode":            s.turnCredentialMode(),
			"credential_ttl_ms":          s.cfg.TURNCredentialTTL.Milliseconds(),
			"relay_min_port":             s.cfg.TURNMinPort,
			"relay_max_port":             s.cfg.TURNMaxPort,
			"credentials_per_minute":     s.cfg.TURNCredentialsPerMinute,
			"active_allocations":         turnStats.Active,
			"max_allocations":            s.cfg.TURNMaxAllocations,
			"max_allocations_per_user":   s.cfg.TURNMaxAllocationsPerUser,
			"max_allocations_per_ip":     s.cfg.TURNMaxAllocationsPerIP,
			"tracked_allocation_users":   turnStats.Users,
			"tracked_allocation_sources": turnStats.IPs,
			"egress_bytes_total":         turnStats.EgressBytes,
			"dropped_bytes_total":        turnStats.DroppedBytes,
			"quota_exceeded_total":       turnStats.QuotaExceeded,
			"egress_window_ms":           s.cfg.TURNEgressWindow.Milliseconds(),
			"max_egress_bytes":           s.cfg.TURNMaxEgressBytes,
			"max_egress_bytes_per_user":  s.cfg.TURNMaxEgressBytesPerUser,
			"max_egress_bytes_per_ip":    s.cfg.TURNMaxEgressBytesPerIP,
			"tracked_traffic_users":      turnStats.TrafficUsers,
			"tracked_traffic_sources":    turnStats.TrafficIPs,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == http.MethodHead {
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

func (s *Server) nativeRelayCredentialMode() string {
	if s.cfg.NativeRelay == "" {
		return "none"
	}
	if s.cfg.NativeRelaySecret != "" {
		return "temporary"
	}
	return "client-configured"
}

func (s *Server) roomStats() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	stats := map[string]int{
		"active":          len(s.rooms),
		"locked":          0,
		"clients":         0,
		"pending_signals": 0,
	}
	for _, room := range s.rooms {
		if room.locked {
			stats["locked"]++
		}
		stats["clients"] += len(room.clients)
		stats["pending_signals"] += len(room.pending)
	}
	return stats
}

func (s *Server) startTURN(ctx context.Context) error {
	if s.cfg.TURNListen == "" {
		return nil
	}
	listener, err := net.ListenPacket("udp4", s.cfg.TURNListen)
	if err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(listener.LocalAddr().String())
	if err != nil {
		_ = listener.Close()
		return err
	}
	listenAddr := host
	if listenAddr == "" || listenAddr == "::" {
		listenAddr = "0.0.0.0"
	}
	publicIP := s.cfg.TURNPublicIP
	if publicIP == "" {
		publicIP = host
	}
	if publicIP == "" || publicIP == "::" || publicIP == "0.0.0.0" {
		publicIP = "127.0.0.1"
	}
	relayIP := net.ParseIP(publicIP)
	if relayIP == nil {
		_ = listener.Close()
		return fmt.Errorf("invalid TURN public IP %q", publicIP)
	}
	staticKey := turn.GenerateAuthKey(s.cfg.TURNUsername, s.cfg.TURNRealm, s.cfg.TURNCredential)
	authHandler := turn.AuthHandler(func(username, realm string, srcAddr net.Addr) ([]byte, bool) {
		return staticKey, username == s.cfg.TURNUsername && realm == s.cfg.TURNRealm
	})
	if s.cfg.TURNSecret != "" {
		authHandler = turn.LongTermTURNRESTAuthHandler(s.cfg.TURNSecret, nil)
	}
	quota := newTURNAllocationQuota(
		s.cfg.TURNMaxAllocations,
		s.cfg.TURNMaxAllocationsPerUser,
		s.cfg.TURNMaxAllocationsPerIP,
	)
	quota.ConfigureTraffic(
		s.cfg.TURNEgressWindow,
		s.cfg.TURNMaxEgressBytes,
		s.cfg.TURNMaxEgressBytesPerUser,
		s.cfg.TURNMaxEgressBytesPerIP,
	)
	baseGenerator := newTURNRelayAddressGenerator(relayIP, listenAddr, s.cfg.TURNMinPort, s.cfg.TURNMaxPort)
	relayGenerator := &turnAccountingRelayAddressGenerator{
		base:  baseGenerator,
		quota: quota,
	}
	server, err := turn.NewServer(turn.ServerConfig{
		Realm:        s.cfg.TURNRealm,
		AuthHandler:  authHandler,
		QuotaHandler: quota.Allow,
		EventHandler: turn.EventHandler{
			OnAllocationCreated: quota.Created,
			OnAllocationDeleted: quota.Deleted,
		},
		PacketConnConfigs: []turn.PacketConnConfig{
			{
				PacketConn: &turnAccountingPacketConn{
					PacketConn: listener,
					quota:      quota,
				},
				RelayAddressGenerator: relayGenerator,
			},
		},
	})
	if err != nil {
		_ = listener.Close()
		return err
	}
	s.turnServer = server
	s.turnQuota = quota
	s.cfg.TURN = "turn:" + net.JoinHostPort(publicIP, port)
	log.Printf("kigo TURN listening on %s advertised as %s", listener.LocalAddr(), s.cfg.TURN)
	go func() {
		<-ctx.Done()
		_ = server.Close()
	}()
	return nil
}

func newTURNRelayAddressGenerator(publicIP net.IP, listenAddr string, minPort, maxPort int) turn.RelayAddressGenerator {
	if minPort != 0 {
		return &turn.RelayAddressGeneratorPortRange{
			RelayAddress: publicIP,
			Address:      listenAddr,
			MinPort:      uint16(minPort),
			MaxPort:      uint16(maxPort),
		}
	}
	return &turn.RelayAddressGeneratorStatic{
		RelayAddress: publicIP,
		Address:      listenAddr,
	}
}

func (s *Server) handleFavicon(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSignal(w http.ResponseWriter, r *http.Request) {
	if !s.allowRequest(r) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/api/signal/")
	if !isRoomToken(token) {
		http.Error(w, "invalid room token", http.StatusBadRequest)
		return
	}
	role := r.URL.Query().Get("role")
	if role != "" && !isSignalRole(role) {
		http.Error(w, "invalid signaling role", http.StatusBadRequest)
		return
	}
	protocol, err := routing.NormalizeProtocol(r.URL.Query().Get("protocol"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	configureSignalConn(conn)
	reconnectToken := ""
	if role != "" && conn.Subprotocol() == signalReconnectProtocol {
		_ = conn.SetReadDeadline(time.Now().Add(signalJoinTimeout))
		var join signalJoin
		if err := conn.ReadJSON(&join); err != nil || join.Type != "signal_join" {
			_ = conn.WriteJSON(map[string]string{"type": "error", "error": "signaling join required"})
			_ = conn.Close()
			return
		}
		reconnectToken = join.ReconnectToken
		_ = conn.SetReadDeadline(time.Now().Add(websocketPongWait))
	} else {
		role = ""
	}
	c, err := s.joinWithRoleProtocol(token, conn, role, reconnectToken, protocol)
	if err != nil {
		_ = conn.WriteJSON(map[string]string{"type": "error", "error": err.Error()})
		_ = conn.Close()
		return
	}
	if role != "" {
		s.observeWebRTCOnlyProtocol(token, role, protocol)
	}
	go c.writeLoop()
	c.readLoop(s)
}

func (s *Server) allow(remote string) bool {
	return s.allowRate(s.signalLimits, remote, s.cfg.SignalRequestsPerMinute)
}

func (s *Server) allowRequest(r *http.Request) bool {
	return s.allow(s.clientAddress(r))
}

func (s *Server) allowICE(remote string) bool {
	return s.allowRate(s.iceLimits, remote, s.cfg.TURNCredentialsPerMinute)
}

func (s *Server) allowICERequest(r *http.Request) bool {
	return s.allowICE(s.clientAddress(r))
}

func (s *Server) allowRate(limits map[string][]time.Time, remote string, maximum int) bool {
	if maximum < 0 {
		return true
	}
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	now := time.Now()
	windowStart := now.Add(-time.Minute)
	s.mu.Lock()
	defer s.mu.Unlock()
	keep := recentRateEvents(limits[host], windowStart)
	if len(keep) >= maximum {
		limits[host] = keep
		return false
	}
	keep = append(keep, now)
	limits[host] = keep
	return true
}

func recentRateEvents(events []time.Time, windowStart time.Time) []time.Time {
	keep := events[:0]
	for _, event := range events {
		if event.After(windowStart) {
			keep = append(keep, event)
		}
	}
	return keep
}

func (s *Server) join(token string, conn *websocket.Conn) (*client, error) {
	return s.joinWithRoleProtocol(token, conn, "", "", negotiationTransfer)
}

func (s *Server) joinWithRole(token string, conn *websocket.Conn, role, reconnectToken string) (*client, error) {
	return s.joinWithRoleProtocol(token, conn, role, reconnectToken, negotiationTransfer)
}

func (s *Server) joinWithRoleProtocol(
	token string,
	conn *websocket.Conn,
	role,
	reconnectToken,
	protocol string,
) (*client, error) {
	if role != "" && !isSignalRole(role) {
		return nil, errors.New("invalid signaling role")
	}
	protocol, err := routing.NormalizeProtocol(protocol)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	r := s.rooms[token]
	if r != nil && now.After(r.expires) {
		s.expireRoomLocked(token, r)
		r = nil
	}
	if r == nil {
		r = &room{
			token:           token,
			protocol:        protocol,
			created:         now,
			expires:         now.Add(s.cfg.RoomTTL),
			clients:         map[*client]bool{},
			slots:           map[string]*client{},
			reconnectTokens: map[string]string{},
		}
		s.rooms[token] = r
	}
	if r.protocol == "" {
		r.protocol = negotiationTransfer
	}
	if r.protocol != protocol {
		return nil, fmt.Errorf("room protocol mismatch: room uses %s, client uses %s", r.protocol, protocol)
	}
	if role == "" {
		return s.joinLegacyLocked(r, conn)
	}
	if r.slots == nil {
		r.slots = map[string]*client{}
	}
	if r.reconnectTokens == nil {
		r.reconnectTokens = map[string]string{}
	}
	if r.locked {
		if r.slots[role] != nil {
			return nil, errors.New(role + " already connected")
		}
		expected := r.reconnectTokens[role]
		if expected == "" || reconnectToken == "" || !equalReconnectToken(expected, reconnectToken) {
			return nil, errors.New("invalid reconnect token")
		}
		if r.needsNewGeneration {
			r.generation++
			r.pending = nil
			r.needsNewGeneration = false
		}
	} else {
		if reconnectToken != "" {
			expected := r.reconnectTokens[role]
			if expected == "" || !equalReconnectToken(expected, reconnectToken) {
				if len(r.clients) == 0 {
					delete(s.rooms, token)
				}
				return nil, errors.New("invalid reconnect token")
			}
		}
		if r.slots[role] != nil {
			return nil, errors.New(role + " already connected")
		}
		for existing := range r.clients {
			if existing.role == "" {
				existing.role = oppositeSignalRole(role)
				r.slots[existing.role] = existing
				break
			}
		}
		if r.reconnectTokens[role] == "" {
			generated, err := generateReconnectToken()
			if err != nil {
				if len(r.clients) == 0 {
					delete(s.rooms, token)
				}
				return nil, err
			}
			r.reconnectTokens[role] = generated
		}
	}
	c := &client{
		room:         r,
		conn:         conn,
		send:         make(chan []byte, signalSendQueueCapacity),
		role:         role,
		explicitRole: true,
	}
	r.clients[c] = true
	r.slots[role] = c
	if len(r.clients) == 2 {
		r.locked = true
		if roomHasTwoExplicitRoles(r) {
			r.reconnectEnabled = true
		}
	}
	reconnectSupported := len(r.clients) < 2 || r.reconnectEnabled
	ready, err := signalReadyPayload(
		role,
		reconnectSupported,
		r.reconnectTokens[role],
		r.generation,
	)
	if err != nil {
		delete(r.clients, c)
		delete(r.slots, role)
		return nil, err
	}
	c.send <- ready
	for _, msg := range r.pending {
		c.send <- msg
	}
	return c, nil
}

func (s *Server) joinLegacyLocked(r *room, conn *websocket.Conn) (*client, error) {
	if r.locked {
		return nil, errors.New("room is locked")
	}
	if len(r.clients) >= 2 {
		return nil, errors.New("room is full")
	}
	c := &client{room: r, conn: conn, send: make(chan []byte, signalSendQueueCapacity)}
	for existing := range r.clients {
		if existing.role != "" {
			c.role = oppositeSignalRole(existing.role)
			r.slots[c.role] = c
			break
		}
	}
	r.clients[c] = true
	if len(r.clients) == 2 {
		r.locked = true
		for existing := range r.clients {
			if !existing.explicitRole {
				continue
			}
			ready, err := signalReadyPayload(existing.role, false, "", r.generation)
			if err == nil {
				existing.send <- ready
			}
		}
	}
	for _, msg := range r.pending {
		c.send <- msg
	}
	return c, nil
}

func isSignalRole(role string) bool {
	return role == "sender" || role == "receiver"
}

func oppositeSignalRole(role string) string {
	if role == "sender" {
		return "receiver"
	}
	return "sender"
}

func generateReconnectToken() (string, error) {
	var token [32]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", fmt.Errorf("generate reconnect token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(token[:]), nil
}

func equalReconnectToken(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func signalReadyPayload(role string, supported bool, reconnectToken string, generation uint64) ([]byte, error) {
	message := map[string]any{
		"type":                "signal_ready",
		"role":                role,
		"reconnect_supported": supported,
		"generation":          generation,
	}
	if supported {
		message["reconnect_token"] = reconnectToken
	}
	return json.Marshal(message)
}

func (c *client) readLoop(s *Server) {
	defer s.leave(c)
	for {
		_, payload, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		if !json.Valid(payload) {
			continue
		}
		s.forward(c, payload)
	}
}

func isRoomToken(token string) bool {
	if len(token) != 64 {
		return false
	}
	for _, r := range token {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			continue
		}
		return false
	}
	return true
}

func configureSignalConn(conn *websocket.Conn) {
	conn.SetReadLimit(maxSignalMessageBytes)
	_ = conn.SetReadDeadline(time.Now().Add(websocketPongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(websocketPongWait))
	})
}

func (c *client) writeLoop() {
	defer c.conn.Close()
	ticker := time.NewTicker(websocketPingPeriod)
	defer ticker.Stop()
	for {
		select {
		case payload, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(websocketWriteWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(websocketWriteWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (s *Server) forward(from *client, payload []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := from.room
	if len(r.pending) >= maxPendingSignals {
		copy(r.pending, r.pending[1:])
		r.pending = r.pending[:maxPendingSignals-1]
	}
	r.pending = append(r.pending, append([]byte(nil), payload...))
	for c := range r.clients {
		if c == from {
			continue
		}
		select {
		case c.send <- payload:
		default:
		}
	}
}

func (s *Server) leave(c *client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.room != nil {
		r := c.room
		delete(r.clients, c)
		if c.role != "" && r.slots[c.role] == c {
			delete(r.slots, c.role)
			if r.locked && c.explicitRole {
				r.needsNewGeneration = true
			}
		}
		if len(r.clients) == 0 && !roomSupportsReconnect(r) {
			delete(s.rooms, r.token)
		}
	}
	closeClient(c)
}

func roomSupportsReconnect(r *room) bool {
	return r.locked && r.reconnectEnabled &&
		r.reconnectTokens["sender"] != "" &&
		r.reconnectTokens["receiver"] != ""
}

func roomHasTwoExplicitRoles(r *room) bool {
	sender := r.slots["sender"]
	receiver := r.slots["receiver"]
	return sender != nil && sender.explicitRole &&
		receiver != nil && receiver.explicitRole
}

func closeClient(c *client) {
	if c.closed {
		return
	}
	c.closed = true
	close(c.send)
}

func (s *Server) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.cleanup()
		}
	}
}

func (s *Server) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.cleanupRateLimitsLocked(now)
	for token, r := range s.rooms {
		if !now.After(r.expires) {
			continue
		}
		s.expireRoomLocked(token, r)
	}
	s.negotiations.expireBefore(now)
	s.directs.expireBefore(now)
}

func (s *Server) cleanupRateLimitsLocked(now time.Time) {
	windowStart := now.Add(-time.Minute)
	for _, limits := range []map[string][]time.Time{s.signalLimits, s.iceLimits} {
		for host, events := range limits {
			keep := recentRateEvents(events, windowStart)
			if len(keep) == 0 {
				delete(limits, host)
				continue
			}
			limits[host] = keep
		}
	}
}

func (s *Server) expireRoomLocked(token string, r *room) {
	expired := []byte(`{"type":"error","error":"room expired"}`)
	for c := range r.clients {
		select {
		case c.send <- expired:
		default:
		}
		closeClient(c)
	}
	delete(s.rooms, token)
}
