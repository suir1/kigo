package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pion/turn/v4"
)

func TestHandleICEReturnsDefaultSTUNAndConfiguredTURN(t *testing.T) {
	s := New(Config{TURN: "turn:turn.example:3478", TURNUsername: "user", TURNCredential: "pass"})
	req := httptest.NewRequest(http.MethodGet, "/api/ice", nil)
	rec := httptest.NewRecorder()

	s.handleICE(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		ICEServers []struct {
			URLs       []string `json:"urls"`
			Username   string   `json:"username"`
			Credential string   `json:"credential"`
		} `json:"iceServers"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.ICEServers) != 2 {
		t.Fatalf("ice server count = %d", len(body.ICEServers))
	}
	if len(body.ICEServers[0].URLs) != 3 ||
		body.ICEServers[0].URLs[0] != "stun:turn.example:3478" ||
		body.ICEServers[0].URLs[1] != "stun:stun.l.google.com:19302" ||
		body.ICEServers[0].URLs[2] != "stun:stun1.l.google.com:19302" {
		t.Fatalf("unexpected stun server: %#v", body.ICEServers[0].URLs)
	}
	if body.ICEServers[1].URLs[0] != "turn:turn.example:3478" {
		t.Fatalf("unexpected turn server: %#v", body.ICEServers[1].URLs)
	}
	if body.ICEServers[1].Username != "user" || body.ICEServers[1].Credential != "pass" {
		t.Fatalf("unexpected turn credentials: %#v", body.ICEServers[1])
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("cache control = %q", rec.Header().Get("Cache-Control"))
	}
}

func TestSTUNURLForTURN(t *testing.T) {
	tests := map[string]string{
		"turn:turn.example:3478":                "stun:turn.example:3478",
		"turn:turn.example:3478?transport=udp":  "stun:turn.example:3478",
		"turns:turn.example:5349?transport=tcp": "stuns:turn.example:5349",
		"stun:stun.example:3478":                "",
		"":                                      "",
	}
	for input, want := range tests {
		if got := stunURLForTURN(input); got != want {
			t.Errorf("stunURLForTURN(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestHandleICEIssuesTemporaryTURNRESTCredentials(t *testing.T) {
	const secret = "temporary-turn-secret"
	s := New(Config{
		TURN:              "turn:turn.example:3478",
		TURNUsername:      "kigo",
		TURNSecret:        secret,
		TURNCredentialTTL: 5 * time.Minute,
	})
	req := httptest.NewRequest(http.MethodGet, "/api/ice", nil)
	rec := httptest.NewRecorder()

	s.handleICE(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		ICEServers []struct {
			Username   string `json:"username"`
			Credential string `json:"credential"`
		} `json:"iceServers"`
		CredentialExpiresAt int64 `json:"credentialExpiresAt"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.ICEServers) != 2 {
		t.Fatalf("ice server count = %d", len(body.ICEServers))
	}
	username := body.ICEServers[1].Username
	parts := strings.SplitN(username, ":", 2)
	if len(parts) != 2 || !strings.HasPrefix(parts[1], "kigo-") {
		t.Fatalf("temporary username = %q", username)
	}
	expires, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if body.CredentialExpiresAt != expires*1000 {
		t.Fatalf("expires_at = %d, username expiry = %d", body.CredentialExpiresAt, expires*1000)
	}
	mac := hmac.New(sha1.New, []byte(secret))
	_, _ = mac.Write([]byte(username))
	wantCredential := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	if body.ICEServers[1].Credential != wantCredential {
		t.Fatal("temporary TURN credential has invalid HMAC")
	}
	auth := turn.LongTermTURNRESTAuthHandler(secret, nil)
	key, ok := auth(username, s.cfg.TURNRealm, &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 1234})
	if !ok {
		t.Fatal("Pion TURN REST handler rejected issued credential")
	}
	wantKey := turn.GenerateAuthKey(username, s.cfg.TURNRealm, wantCredential)
	if !bytes.Equal(key, wantKey) {
		t.Fatal("Pion TURN REST auth key did not match issued credential")
	}
}

func TestHandleICERateLimitAndMethods(t *testing.T) {
	s := New(Config{TURNCredentialsPerMinute: 2})
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		s.handleICE(rec, httptest.NewRequest(http.MethodGet, "/api/ice", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status = %d", i, rec.Code)
		}
	}
	limited := httptest.NewRecorder()
	s.handleICE(limited, httptest.NewRequest(http.MethodGet, "/api/ice", nil))
	if limited.Code != http.StatusTooManyRequests {
		t.Fatalf("limited status = %d", limited.Code)
	}
	otherRequest := httptest.NewRequest(http.MethodGet, "/api/ice", nil)
	otherRequest.RemoteAddr = "192.0.2.2:1234"
	other := httptest.NewRecorder()
	s.handleICE(other, otherRequest)
	if other.Code != http.StatusOK {
		t.Fatalf("other IP status = %d", other.Code)
	}
	post := httptest.NewRecorder()
	s.handleICE(post, httptest.NewRequest(http.MethodPost, "/api/ice", nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", post.Code)
	}
	headRequest := httptest.NewRequest(http.MethodHead, "/api/ice", nil)
	headRequest.RemoteAddr = "192.0.2.3:1234"
	head := httptest.NewRecorder()
	s.handleICE(head, headRequest)
	if head.Code != http.StatusOK || head.Body.Len() != 0 {
		t.Fatalf("HEAD status=%d body=%q", head.Code, head.Body.String())
	}
}

func TestHandleHealthReturnsRuntimeStatusWithoutSecrets(t *testing.T) {
	s := New(Config{
		PublicURL:                "https://kigo.example",
		NoteStore:                t.TempDir(),
		NoteTTL:                  2 * time.Hour,
		NativeRelay:              "relay.example:9000",
		NativeRelaySecret:        "native-relay-secret",
		NativeRelayCredentialTTL: 30 * time.Minute,
		TURN:                     "turn:turn.example:3478",
		TURNListen:               "0.0.0.0:3478",
		TURNUsername:             "user",
		TURNCredential:           "secret",
	})
	client, err := s.join("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.leave(client)

	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	s.handleHealth(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct {
		OK        bool   `json:"ok"`
		PublicURL string `json:"public_url"`
		Version   struct {
			Version string `json:"version"`
			Go      string `json:"go"`
		} `json:"version"`
		Capabilities []string `json:"capabilities"`
		Rooms        struct {
			Active  int `json:"active"`
			Clients int `json:"clients"`
		} `json:"rooms"`
		Notepad struct {
			Configured bool  `json:"configured"`
			Documents  int   `json:"documents"`
			Clients    int   `json:"clients"`
			TTLMS      int64 `json:"ttl_ms"`
		} `json:"notepad"`
		NativeRelay struct {
			Configured      bool   `json:"configured"`
			Endpoint        string `json:"endpoint"`
			CredentialMode  string `json:"credential_mode"`
			CredentialTTLMS int64  `json:"credential_ttl_ms"`
		} `json:"native_relay"`
		TURN struct {
			Configured       bool   `json:"configured"`
			BuiltIn          bool   `json:"built_in"`
			URL              string `json:"url"`
			CredentialMode   string `json:"credential_mode"`
			MaxAllocations   int    `json:"max_allocations"`
			MaxPerUser       int    `json:"max_allocations_per_user"`
			MaxPerIP         int    `json:"max_allocations_per_ip"`
			ActiveAllocation int    `json:"active_allocations"`
			EgressWindowMS   int64  `json:"egress_window_ms"`
			MaxEgressBytes   int64  `json:"max_egress_bytes"`
			EgressBytes      int64  `json:"egress_bytes_total"`
			DroppedBytes     int64  `json:"dropped_bytes_total"`
			QuotaExceeded    int64  `json:"quota_exceeded_total"`
		} `json:"turn"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.PublicURL != "https://kigo.example" {
		t.Fatalf("unexpected health body: %#v", body)
	}
	if body.Version.Version == "" || body.Version.Go == "" {
		t.Fatalf("missing version info: %#v", body.Version)
	}
	if len(body.Capabilities) != 4 || body.Capabilities[1] != "direct-rendezvous-v1" || body.Capabilities[3] != "persistent-note-v1" {
		t.Fatalf("capabilities = %#v", body.Capabilities)
	}
	if body.Rooms.Active != 1 || body.Rooms.Clients != 1 {
		t.Fatalf("room stats = %#v", body.Rooms)
	}
	if !body.Notepad.Configured || body.Notepad.Documents != 0 || body.Notepad.Clients != 0 || body.Notepad.TTLMS != (2*time.Hour).Milliseconds() {
		t.Fatalf("notepad stats = %#v", body.Notepad)
	}
	if !body.NativeRelay.Configured ||
		body.NativeRelay.Endpoint != "relay.example:9000" ||
		body.NativeRelay.CredentialMode != "temporary" ||
		body.NativeRelay.CredentialTTLMS != (30*time.Minute).Milliseconds() {
		t.Fatalf("native relay = %#v", body.NativeRelay)
	}
	if !body.TURN.Configured || !body.TURN.BuiltIn || body.TURN.URL != "turn:turn.example:3478" {
		t.Fatalf("turn = %#v", body.TURN)
	}
	if body.TURN.CredentialMode != "static" ||
		body.TURN.MaxAllocations != 1024 ||
		body.TURN.MaxPerUser != 4 ||
		body.TURN.MaxPerIP != 32 ||
		body.TURN.ActiveAllocation != 0 ||
		body.TURN.EgressWindowMS != time.Hour.Milliseconds() ||
		body.TURN.MaxEgressBytes != -1 ||
		body.TURN.EgressBytes != 0 ||
		body.TURN.DroppedBytes != 0 ||
		body.TURN.QuotaExceeded != 0 {
		t.Fatalf("turn controls = %#v", body.TURN)
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatal("health response leaked a service credential")
	}
}

func TestHandleHealthSupportsHeadAndRejectsPost(t *testing.T) {
	s := New(Config{})
	head := httptest.NewRecorder()
	s.handleHealth(head, httptest.NewRequest(http.MethodHead, "/api/health", nil))
	if head.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d", head.Code)
	}
	if head.Body.Len() != 0 {
		t.Fatalf("HEAD body length = %d", head.Body.Len())
	}

	post := httptest.NewRecorder()
	s.handleHealth(post, httptest.NewRequest(http.MethodPost, "/api/health", nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", post.Code)
	}
}

func TestAllowRateLimitsByRemoteHost(t *testing.T) {
	s := New(Config{SignalRequestsPerMinute: 2})
	for i := 0; i < 2; i++ {
		if !s.allow("127.0.0.1:1234") {
			t.Fatalf("request %d was unexpectedly rate limited", i)
		}
	}
	if s.allow("127.0.0.1:1234") {
		t.Fatal("expected request 3 to be rate limited")
	}
	if !s.allow("127.0.0.2:1234") {
		t.Fatal("different host should not be rate limited")
	}
}

func TestCleanupRemovesExpiredRateLimitSources(t *testing.T) {
	s := New(Config{})
	now := time.Now()
	s.signalLimits["expired"] = []time.Time{now.Add(-2 * time.Minute)}
	s.signalLimits["active"] = []time.Time{now.Add(-30 * time.Second)}
	s.iceLimits["expired"] = []time.Time{now.Add(-time.Hour)}

	s.cleanup()
	if _, ok := s.signalLimits["expired"]; ok {
		t.Fatal("expired signaling source was retained")
	}
	if len(s.signalLimits["active"]) != 1 {
		t.Fatalf("active signaling events = %#v", s.signalLimits["active"])
	}
	if _, ok := s.iceLimits["expired"]; ok {
		t.Fatal("expired ICE source was retained")
	}
}

func TestTURNAllocationQuotaTracksGlobalUserAndIPLimits(t *testing.T) {
	q := newTURNAllocationQuota(2, 1, 1)
	firstAddr := &net.UDPAddr{IP: net.ParseIP("192.0.2.1"), Port: 1000}
	secondAddr := &net.UDPAddr{IP: net.ParseIP("192.0.2.2"), Port: 1001}
	if !q.Allow("user-1", "kigo", firstAddr) {
		t.Fatal("first allocation was rejected")
	}
	q.Created(firstAddr, nil, "udp", "user-1", "kigo", nil, 0)
	if q.Allow("user-1", "kigo", secondAddr) {
		t.Fatal("per-user quota was not enforced")
	}
	if q.Allow("user-2", "kigo", firstAddr) {
		t.Fatal("per-IP quota was not enforced")
	}
	if !q.Allow("user-2", "kigo", secondAddr) {
		t.Fatal("second allocation was rejected")
	}
	q.Created(secondAddr, nil, "udp", "user-2", "kigo", nil, 0)
	if q.Allow("user-3", "kigo", &net.UDPAddr{IP: net.ParseIP("192.0.2.3"), Port: 1002}) {
		t.Fatal("global quota was not enforced")
	}
	stats := q.Stats()
	if stats.Active != 2 || stats.Users != 2 || stats.IPs != 2 {
		t.Fatalf("quota stats = %#v", stats)
	}
	q.Deleted(firstAddr, nil, "udp", "user-1", "kigo")
	if !q.Allow("user-1", "kigo", firstAddr) {
		t.Fatal("deleted allocation did not release quota")
	}
	stats = q.Stats()
	if stats.Active != 1 || stats.Users != 1 || stats.IPs != 1 {
		t.Fatalf("quota stats after delete = %#v", stats)
	}
}

func TestTURNEgressQuotaTracksAndRefillsAtomically(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	q := newTURNAllocationQuota(-1, -1, -1)
	q.ConfigureTraffic(time.Hour, 100, 80, 70)
	q.now = func() time.Time { return now }

	client := &net.UDPAddr{IP: net.ParseIP("192.0.2.10"), Port: 1000}
	relay := &net.UDPAddr{IP: net.ParseIP("198.51.100.10"), Port: 5000}
	q.Created(client, nil, "udp", "user-1", "kigo", relay, 0)

	if !q.ConsumeRelay(addressKey(relay), 60) {
		t.Fatal("initial relay egress was rejected")
	}
	if q.ConsumeClient(client, 15) {
		t.Fatal("per-IP quota was not enforced")
	}
	stats := q.Stats()
	if stats.EgressBytes != 60 || stats.DroppedBytes != 15 || stats.QuotaExceeded != 1 {
		t.Fatalf("traffic stats after rejection = %#v", stats)
	}

	now = now.Add(30 * time.Minute)
	if !q.ConsumeClient(client, 15) {
		t.Fatal("refilled quota rejected valid egress")
	}
	if stats := q.Stats(); stats.EgressBytes != 75 {
		t.Fatalf("egress after refill = %#v", stats)
	}
}

func TestTURNEgressQuotaSharesUserAndIPBucketsAcrossAllocations(t *testing.T) {
	q := newTURNAllocationQuota(-1, -1, -1)
	q.ConfigureTraffic(time.Hour, -1, 100, 90)
	q.now = func() time.Time { return time.Unix(1_700_000_000, 0) }

	firstClient := &net.UDPAddr{IP: net.ParseIP("192.0.2.20"), Port: 1000}
	secondClient := &net.UDPAddr{IP: net.ParseIP("192.0.2.20"), Port: 1001}
	firstRelay := &net.UDPAddr{IP: net.ParseIP("198.51.100.20"), Port: 5000}
	secondRelay := &net.UDPAddr{IP: net.ParseIP("198.51.100.20"), Port: 5001}
	q.Created(firstClient, nil, "udp", "shared-user", "kigo", firstRelay, 0)
	q.Created(secondClient, nil, "udp", "shared-user", "kigo", secondRelay, 0)

	if !q.ConsumeRelay(addressKey(firstRelay), 70) {
		t.Fatal("first allocation egress was rejected")
	}
	if q.ConsumeRelay(addressKey(secondRelay), 25) {
		t.Fatal("shared source-IP quota was not enforced")
	}
	if stats := q.Stats(); stats.EgressBytes != 70 || stats.DroppedBytes != 25 {
		t.Fatalf("shared quota stats = %#v", stats)
	}
}

func TestTURNAccountingPacketConnDropsPacketAtQuota(t *testing.T) {
	q := newTURNAllocationQuota(-1, -1, -1)
	q.ConfigureTraffic(time.Hour, 8, -1, -1)
	client := &net.UDPAddr{IP: net.ParseIP("192.0.2.30"), Port: 1000}
	relay := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5000}
	q.Created(client, nil, "udp", "user-1", "kigo", relay, 0)

	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	peer, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	accounted := &turnAccountingPacketConn{
		PacketConn:  conn,
		quota:       q,
		lookupRelay: true,
		key:         addressKey(relay),
	}
	if n, err := accounted.WriteTo(make([]byte, 9), peer.LocalAddr()); err != nil || n != 9 {
		t.Fatalf("dropped write n=%d err=%v", n, err)
	}
	if err := peer.SetReadDeadline(time.Now().Add(25 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := peer.ReadFrom(make([]byte, 16)); err == nil {
		t.Fatal("quota-exceeded packet reached peer")
	}
	if stats := q.Stats(); stats.EgressBytes != 0 || stats.DroppedBytes != 9 || stats.QuotaExceeded != 1 {
		t.Fatalf("quota rejection stats = %#v", stats)
	}
}

func TestBuiltInTURNAcceptsIssuedTemporaryCredentials(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	relayReservation, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	relayPort := relayReservation.LocalAddr().(*net.UDPAddr).Port
	s := New(Config{
		TURNListen:                "127.0.0.1:0",
		TURNPublicIP:              "127.0.0.1",
		TURNMinPort:               relayPort,
		TURNMaxPort:               relayPort,
		TURNSecret:                "integration-turn-secret",
		TURNRealm:                 "kigo-test",
		TURNCredentialTTL:         time.Minute,
		TURNMaxAllocations:        4,
		TURNMaxAllocationsPerUser: 2,
		TURNMaxAllocationsPerIP:   2,
		TURNEgressWindow:          time.Minute,
		TURNMaxEgressBytes:        1 << 20,
		TURNMaxEgressBytesPerUser: 1 << 20,
		TURNMaxEgressBytesPerIP:   1 << 20,
	})
	if err := s.startTURN(ctx); err != nil {
		_ = relayReservation.Close()
		cancel()
		t.Fatal(err)
	}
	defer func() {
		cancel()
		if s.turnServer != nil {
			_ = s.turnServer.Close()
		}
	}()

	username, credential, _, err := s.turnCredentials(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	turnAddr := strings.TrimPrefix(s.cfg.TURN, "turn:")
	conn, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	client, err := turn.NewClient(&turn.ClientConfig{
		Conn:           conn,
		STUNServerAddr: turnAddr,
		TURNServerAddr: turnAddr,
		Username:       username,
		Password:       credential,
		Realm:          s.cfg.TURNRealm,
		RTO:            50 * time.Millisecond,
	})
	if err != nil {
		_ = conn.Close()
		t.Fatal(err)
	}
	if err := client.Listen(); err != nil {
		client.Close()
		_ = conn.Close()
		t.Fatal(err)
	}
	if err := relayReservation.Close(); err != nil {
		client.Close()
		_ = conn.Close()
		t.Fatal(err)
	}
	allocation, err := client.Allocate()
	if err != nil {
		client.Close()
		_ = conn.Close()
		t.Fatal(err)
	}
	if got := allocation.LocalAddr().(*net.UDPAddr).Port; got != relayPort {
		t.Fatalf("TURN relay port = %d, want %d", got, relayPort)
	}
	if stats := s.turnStats(); stats.Active != 1 || stats.Users != 1 || stats.IPs != 1 {
		t.Fatalf("active TURN stats = %#v", stats)
	}
	peer, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	payload := []byte("counted TURN payload")
	if _, err := allocation.WriteTo(payload, peer.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 1024)
	if err := peer.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	n, _, err := peer.ReadFrom(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buffer[:n], payload) {
		t.Fatalf("peer payload = %q", buffer[:n])
	}
	reply := []byte("counted TURN reply")
	if _, err := peer.WriteTo(reply, allocation.LocalAddr()); err != nil {
		t.Fatal(err)
	}
	if err := allocation.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	n, _, err = allocation.ReadFrom(buffer)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buffer[:n], reply) {
		t.Fatalf("allocation reply = %q", buffer[:n])
	}
	if stats := s.turnStats(); stats.EgressBytes < int64(len(payload)+len(reply)) {
		t.Fatalf("TURN egress was not counted: %#v", stats)
	}
	if err := allocation.Close(); err != nil {
		t.Fatal(err)
	}
	client.Close()
	_ = conn.Close()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && s.turnStats().Active != 0 {
		time.Sleep(time.Millisecond)
	}
	if stats := s.turnStats(); stats.Active != 0 {
		t.Fatalf("TURN allocation was not released: %#v", stats)
	}
}

func TestValidateServiceConfigRejectsUnsafeValues(t *testing.T) {
	tests := []Config{
		{SignalRequestsPerMinute: -2},
		{NoteTTL: -time.Second},
		{NoteUpdatesPerMinute: -2},
		{TURNCredentialTTL: 30 * time.Second},
		{TURNCredentialTTL: maxTURNCredentialTTL + time.Second},
		{TURNCredentialsPerMinute: -2},
		{TURNMaxAllocations: -2},
		{TURNSecret: "secret"},
		{TURNEgressWindow: 30 * time.Second},
		{TURNMaxEgressBytes: -2},
		{TURN: "turn:turn.example:3478", TURNMaxEgressBytes: 1024},
		{TURNListen: "127.0.0.1:3478", TURNMinPort: 49160},
		{TURNMinPort: 49160, TURNMaxPort: 49259},
		{TURNListen: "127.0.0.1:3478", TURNMinPort: 49259, TURNMaxPort: 49160},
		{TURNListen: "127.0.0.1:3478", TURNMinPort: 65535, TURNMaxPort: 65535},
		{TURNListen: "127.0.0.1:3478", TURNPublicIP: "turn.example"},
	}
	for _, cfg := range tests {
		if err := New(cfg).validateConfig(); err == nil {
			t.Fatalf("config was accepted: %#v", cfg)
		}
	}
}

func TestTURNPortRangeSelectsBoundedGenerator(t *testing.T) {
	s := New(Config{
		TURNListen:   "127.0.0.1:3478",
		TURNPublicIP: "127.0.0.1",
		TURNMinPort:  49160,
		TURNMaxPort:  49259,
	})
	if err := s.validateConfig(); err != nil {
		t.Fatal(err)
	}
	base := newTURNRelayAddressGenerator(net.ParseIP(s.cfg.TURNPublicIP), "127.0.0.1", s.cfg.TURNMinPort, s.cfg.TURNMaxPort)
	ranged, ok := base.(*turn.RelayAddressGeneratorPortRange)
	if !ok || ranged.MinPort != 49160 || ranged.MaxPort != 49259 {
		t.Fatalf("generator = %#v", base)
	}
}

func TestHandleFaviconReturnsNoContent(t *testing.T) {
	s := New(Config{})
	req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
	rec := httptest.NewRecorder()

	s.handleFavicon(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestHandleSignalRejectsInvalidRoomToken(t *testing.T) {
	s := New(Config{})
	tests := []string{
		"/api/signal/short",
		"/api/signal/zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
		"/api/signal/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdeg",
		"/api/signal/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef/extra",
	}
	for _, path := range tests {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()

			s.handleSignal(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d", rec.Code)
			}
		})
	}
}

func TestJoinLocksRoomAtTwoClients(t *testing.T) {
	s := New(Config{})
	first, err := s.join("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.join("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.join("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", nil); err == nil {
		t.Fatal("third client joined full room")
	}
	s.leave(first)
	s.leave(second)
}

func TestSignalingRoomRejectsProtocolMismatch(t *testing.T) {
	s := New(Config{})
	token := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	noteClient, err := s.joinWithRoleProtocol(token, nil, "sender", "", negotiationNote)
	if err != nil {
		t.Fatal(err)
	}
	defer s.leave(noteClient)

	if _, err := s.joinWithRoleProtocol(
		token,
		nil,
		"receiver",
		"",
		negotiationTransfer,
	); err == nil || !strings.Contains(err.Error(), "protocol mismatch") {
		t.Fatalf("signaling protocol mismatch error = %v", err)
	}
}

func TestRoleJoinIssuesReconnectTokenAndLocksSlots(t *testing.T) {
	s := New(Config{})
	token := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	sender, err := s.joinWithRole(token, nil, "sender", "")
	if err != nil {
		t.Fatal(err)
	}
	senderReady := readSignalReady(t, sender)
	if senderReady.Role != "sender" || senderReady.ReconnectToken == "" || !senderReady.ReconnectSupported {
		t.Fatalf("unexpected sender ready: %#v", senderReady)
	}
	receiver, err := s.joinWithRole(token, nil, "receiver", "")
	if err != nil {
		t.Fatal(err)
	}
	receiverReady := readSignalReady(t, receiver)
	if receiverReady.Role != "receiver" || receiverReady.ReconnectToken == "" {
		t.Fatalf("unexpected receiver ready: %#v", receiverReady)
	}
	if senderReady.ReconnectToken == receiverReady.ReconnectToken {
		t.Fatal("sender and receiver received the same reconnect token")
	}
	if _, err := s.joinWithRole(token, nil, "sender", senderReady.ReconnectToken); err == nil {
		t.Fatal("occupied sender role was replaced")
	}
	s.leave(sender)
	s.leave(receiver)
}

func TestRoleReconnectRequiresMatchingToken(t *testing.T) {
	s := New(Config{})
	token := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	sender, err := s.joinWithRole(token, nil, "sender", "")
	if err != nil {
		t.Fatal(err)
	}
	senderReady := readSignalReady(t, sender)
	receiver, err := s.joinWithRole(token, nil, "receiver", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = readSignalReady(t, receiver)
	s.leave(sender)

	if _, err := s.joinWithRole(token, nil, "sender", ""); err == nil {
		t.Fatal("sender reconnected without a token")
	}
	if _, err := s.joinWithRole(token, nil, "sender", strings.Repeat("x", 43)); err == nil {
		t.Fatal("sender reconnected with the wrong token")
	}
	rejoined, err := s.joinWithRole(token, nil, "sender", senderReady.ReconnectToken)
	if err != nil {
		t.Fatal(err)
	}
	ready := readSignalReady(t, rejoined)
	if ready.Generation != 1 {
		t.Fatalf("reconnect generation = %d, want 1", ready.Generation)
	}
	s.leave(rejoined)
	s.leave(receiver)
}

func TestRoleReconnectClearsStaleSignalsOnlyOncePerGeneration(t *testing.T) {
	s := New(Config{})
	token := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	sender, err := s.joinWithRole(token, nil, "sender", "")
	if err != nil {
		t.Fatal(err)
	}
	senderReady := readSignalReady(t, sender)
	receiver, err := s.joinWithRole(token, nil, "receiver", "")
	if err != nil {
		t.Fatal(err)
	}
	receiverReady := readSignalReady(t, receiver)
	s.forward(sender, []byte(`{"type":"offer","sdp":"stale"}`))
	s.leave(sender)
	s.leave(receiver)

	rejoinedSender, err := s.joinWithRole(token, nil, "sender", senderReady.ReconnectToken)
	if err != nil {
		t.Fatal(err)
	}
	if ready := readSignalReady(t, rejoinedSender); ready.Generation != 1 {
		t.Fatalf("sender reconnect generation = %d, want 1", ready.Generation)
	}
	s.forward(rejoinedSender, []byte(`{"type":"offer","sdp":"fresh"}`))

	rejoinedReceiver, err := s.joinWithRole(token, nil, "receiver", receiverReady.ReconnectToken)
	if err != nil {
		t.Fatal(err)
	}
	if ready := readSignalReady(t, rejoinedReceiver); ready.Generation != 1 {
		t.Fatalf("receiver reconnect generation = %d, want 1", ready.Generation)
	}
	select {
	case payload := <-rejoinedReceiver.send:
		if string(payload) != `{"type":"offer","sdp":"fresh"}` {
			t.Fatalf("receiver replayed %s, want fresh offer", payload)
		}
	default:
		t.Fatal("fresh offer was cleared by second role reconnect")
	}
	s.leave(rejoinedSender)
	s.leave(rejoinedReceiver)
}

func TestRoleReconnectRoomExpiresWithTokens(t *testing.T) {
	s := New(Config{RoomTTL: time.Minute})
	token := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	sender, err := s.joinWithRole(token, nil, "sender", "")
	if err != nil {
		t.Fatal(err)
	}
	senderReady := readSignalReady(t, sender)
	receiver, err := s.joinWithRole(token, nil, "receiver", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = readSignalReady(t, receiver)
	s.leave(sender)
	s.leave(receiver)
	s.rooms[token].expires = time.Now().Add(-time.Second)

	if _, err := s.joinWithRole(token, nil, "sender", senderReady.ReconnectToken); err == nil {
		t.Fatal("expired reconnect token was accepted")
	}
}

func TestLegacyPeerDisablesRoleReconnect(t *testing.T) {
	s := New(Config{})
	token := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	sender, err := s.joinWithRole(token, nil, "sender", "")
	if err != nil {
		t.Fatal(err)
	}
	if ready := readSignalReady(t, sender); !ready.ReconnectSupported {
		t.Fatal("first role client did not receive provisional reconnect support")
	}
	legacy, err := s.join(token, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ready := readSignalReady(t, sender); ready.ReconnectSupported || ready.ReconnectToken != "" {
		t.Fatalf("legacy peer did not disable reconnect: %#v", ready)
	}
	s.leave(sender)
	s.leave(legacy)
}

type signalReadyMessage struct {
	Type               string `json:"type"`
	Role               string `json:"role"`
	ReconnectSupported bool   `json:"reconnect_supported"`
	ReconnectToken     string `json:"reconnect_token"`
	Generation         uint64 `json:"generation"`
}

func readSignalReady(t *testing.T, c *client) signalReadyMessage {
	t.Helper()
	select {
	case payload := <-c.send:
		var ready signalReadyMessage
		if err := json.Unmarshal(payload, &ready); err != nil {
			t.Fatal(err)
		}
		if ready.Type != "signal_ready" {
			t.Fatalf("signal type = %q, want signal_ready", ready.Type)
		}
		return ready
	default:
		t.Fatal("signal_ready was not queued")
		return signalReadyMessage{}
	}
}

func TestJoinReplaysFullPendingSignalBacklogWithoutBlocking(t *testing.T) {
	s := New(Config{})
	token := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	first, err := s.join(token, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxPendingSignals; i++ {
		s.forward(first, []byte(`{"type":"candidate"}`))
	}

	type joinResult struct {
		client *client
		err    error
	}
	joined := make(chan joinResult, 1)
	go func() {
		second, err := s.join(token, nil)
		joined <- joinResult{client: second, err: err}
	}()

	var second *client
	select {
	case result := <-joined:
		if result.err != nil {
			t.Fatal(result.err)
		}
		second = result.client
	case <-time.After(time.Second):
		t.Fatal("second client join blocked while replaying pending signals")
	}
	if got := len(second.send); got != maxPendingSignals {
		t.Fatalf("replayed pending signals = %d, want %d", got, maxPendingSignals)
	}

	s.leave(first)
	s.leave(second)
}

func TestJoinedRoomStaysLockedUntilEmpty(t *testing.T) {
	s := New(Config{})
	first, err := s.join("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.join("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.leave(second)

	if _, err := s.join("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", nil); err == nil {
		t.Fatal("new client joined locked room")
	}

	s.leave(first)
	next, err := s.join("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.leave(next)
}

func TestLeaveDeletesEmptyRoomAndPendingSignals(t *testing.T) {
	s := New(Config{})
	first, err := s.join("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.forward(first, []byte(`{"type":"offer","sdp":"old"}`))
	if len(s.rooms["0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"].pending) != 1 {
		t.Fatal("pending signal was not recorded")
	}
	s.leave(first)
	if _, ok := s.rooms["0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"]; ok {
		t.Fatal("empty room was not deleted")
	}
	next, err := s.join("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.send) != 0 {
		t.Fatal("new room inherited old pending signal")
	}
	s.leave(next)
}

func TestCleanupExpiresActiveRooms(t *testing.T) {
	s := New(Config{RoomTTL: time.Millisecond})
	first, err := s.join("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.rooms["0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"].expires = time.Now().Add(-time.Second)

	s.cleanup()

	if _, ok := s.rooms["0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"]; ok {
		t.Fatal("expired room was not deleted")
	}
	msg, ok := <-first.send
	if !ok {
		t.Fatal("client channel closed before expiration error was queued")
	}
	if string(msg) != `{"type":"error","error":"room expired"}` {
		t.Fatalf("unexpected expiration message: %s", msg)
	}
	if _, ok := <-first.send; ok {
		t.Fatal("client channel was not closed")
	}
}

func TestJoinReplacesExpiredRoom(t *testing.T) {
	s := New(Config{RoomTTL: time.Minute})
	first, err := s.join("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.forward(first, []byte(`{"type":"offer","sdp":"old"}`))
	s.rooms["0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"].expires = time.Now().Add(-time.Second)

	next, err := s.join("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", nil)
	if err != nil {
		t.Fatal(err)
	}

	if next == first {
		t.Fatal("expired room reused old client")
	}
	if len(next.send) != 0 {
		t.Fatal("new room inherited pending signals")
	}
	if _, ok := <-first.send; !ok {
		t.Fatal("old client did not receive expiration message before close")
	}
	s.leave(next)
}

func TestJoinReplacesExpiredLockedRoom(t *testing.T) {
	s := New(Config{RoomTTL: time.Minute})
	first, err := s.join("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.join("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", nil)
	if err != nil {
		t.Fatal(err)
	}
	s.rooms["0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"].expires = time.Now().Add(-time.Second)

	next, err := s.join("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", nil)
	if err != nil {
		t.Fatal(err)
	}

	if next == first || next == second {
		t.Fatal("expired locked room reused old client")
	}
	if _, ok := <-first.send; !ok {
		t.Fatal("first old client did not receive expiration message before close")
	}
	if _, ok := <-second.send; !ok {
		t.Fatal("second old client did not receive expiration message before close")
	}
	s.leave(next)
}

func TestExpiredRoomNotifiesWebSocketClient(t *testing.T) {
	s := New(Config{RoomTTL: time.Minute})
	mux := http.NewServeMux()
	mux.HandleFunc("/api/signal/", s.handleSignal)
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	wsURL := "ws" + server.URL[len("http"):] + "/api/signal/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitForRoom(t, s, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	s.mu.Lock()
	s.rooms["0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"].expires = time.Now().Add(-time.Second)
	s.mu.Unlock()

	s.cleanup()

	_ = conn.SetReadDeadline(time.Now().Add(time.Second))
	var msg map[string]string
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatal(err)
	}
	if msg["type"] != "error" || msg["error"] != "room expired" {
		t.Fatalf("unexpected expiration message: %#v", msg)
	}
}

func TestSignalReadLimitClosesRoom(t *testing.T) {
	s := New(Config{RoomTTL: time.Minute})
	mux := http.NewServeMux()
	mux.HandleFunc("/api/signal/", s.handleSignal)
	server := httptest.NewServer(mux)
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	wsURL := "ws" + server.URL[len("http"):] + "/api/signal/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	waitForRoom(t, s, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

	oversized := strings.Repeat("x", maxSignalMessageBytes+1)
	if err := conn.WriteMessage(websocket.TextMessage, []byte(oversized)); err != nil {
		t.Fatal(err)
	}
	waitForNoRoom(t, s, "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
}

func waitForRoom(t *testing.T, s *Server, token string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		_, ok := s.rooms[token]
		s.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("room %s was not created", token)
}

func waitForNoRoom(t *testing.T, s *Server, token string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		_, ok := s.rooms[token]
		s.mu.Unlock()
		if !ok {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("room %s was not deleted", token)
}
