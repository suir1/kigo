package app

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/suir1/kigo/internal/relay"
	"github.com/suir1/kigo/internal/transport"
)

func TestRouteHistoryRecordsSuccessFailureAndThroughput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "route-history.json")
	if err := recordRouteObservation(path, routeObservation{
		Kind:          historyRouteDirect,
		Success:       true,
		SentBytes:     3 << 20,
		ReceivedBytes: 1 << 20,
		Duration:      2 * time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	if err := recordRouteObservation(path, routeObservation{
		Kind:     historyRouteDirect,
		Success:  false,
		Duration: time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	history, err := loadRouteHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := history.Profiles[defaultRouteHistoryScope].Routes[historyRouteDirect]
	if entry.Attempts != 2 || entry.Successes != 1 || entry.Failures != 1 || entry.ConsecutiveFailures != 1 {
		t.Fatalf("entry = %#v", entry)
	}
	if entry.EWMABytesPerSecond != 2<<20 {
		t.Fatalf("throughput = %f", entry.EWMABytesPerSecond)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"room_token", "pairing", "file_name"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("history contains forbidden field %q", forbidden)
		}
	}
}

func TestRouteHistoryCanChangePrimaryScore(t *testing.T) {
	routes := []routeCandidate{
		{
			Pair:      "native-native",
			Kind:      routeDirectRelayFallback,
			Name:      "direct",
			Score:     92,
			Available: true,
			Primary:   true,
		},
		{
			Pair:      "native-native",
			Kind:      routeRelayOnly,
			Name:      "relay",
			Score:     72,
			Available: true,
		},
	}
	report := doctorHistoryReport{
		Enabled: true,
		Routes: map[string]routeHistorySummary{
			historyRouteDirect: {
				Attempts:            4,
				Successes:           1,
				Failures:            3,
				SuccessRate:         0.25,
				ConsecutiveFailures: 3,
			},
			historyRouteRelay: {
				Attempts:    4,
				Successes:   4,
				SuccessRate: 1,
			},
		},
	}
	applyRouteHistory(routes, report)
	if routes[0].Score >= routes[1].Score {
		t.Fatalf("direct score=%d relay score=%d", routes[0].Score, routes[1].Score)
	}
	if routes[0].Primary || !routes[1].Primary {
		t.Fatalf("primary flags direct=%v relay=%v", routes[0].Primary, routes[1].Primary)
	}
	if routes[0].History == nil || routes[1].History == nil {
		t.Fatal("history summaries not attached")
	}
}

func TestRunTransferRecordsObservedRoute(t *testing.T) {
	path := filepath.Join(t.TempDir(), "route-history.json")
	g := &globalOptions{
		Relay:             "relay.test:9000",
		ReconnectAttempts: 1,
		RouteHistory:      path,
	}
	scope := detectRouteNetworkScope(g)
	err := runTransferWithReconnect(
		context.Background(),
		g,
		func(context.Context) (transport.Transport, error) {
			return transport.Observe(&stubTransport{}, transport.RouteInfo{
				Kind:        historyRouteRelay,
				Connections: 2,
			}), nil
		},
		func(ctx context.Context, t transport.Transport) error {
			transport.RecordPhysicalPathStats(t, []transport.PhysicalPathStats{{
				Connection: 1,
				SentBytes:  int64(len("payload")),
				SentChunks: 1,
				SendNanos:  int64(time.Millisecond),
			}})
			return t.Send(ctx, []byte("payload"))
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	history, err := loadRouteHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := history.Profiles[scope.ID].Routes[historyRouteRelay]
	if entry.Attempts != 1 || entry.Successes != 1 || entry.SentBytes != int64(len("payload")) {
		t.Fatalf("entry = %#v", entry)
	}
	if entry.Paths["1"].Samples != 1 {
		t.Fatalf("path entry = %#v", entry.Paths["1"])
	}
}

func TestInspectRouteHistoryReportsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "route-history.json")
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	report := inspectRouteHistory(&globalOptions{RouteHistory: path})
	if !report.Enabled || report.Error == "" {
		t.Fatalf("report = %#v", report)
	}
}

func TestInspectRouteHistorySelectsCurrentNetworkProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "route-history.json")
	g := &globalOptions{RouteHistory: path, Signal: "http://127.0.0.1:8080"}
	scope := detectRouteNetworkScope(g)
	if err := recordRouteObservation(path, routeObservation{
		Kind:     historyRouteWebRTC,
		ScopeID:  scope.ID,
		Success:  true,
		Duration: time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	if err := recordRouteObservation(path, routeObservation{
		Kind:     historyRouteDirect,
		ScopeID:  "another-network",
		Success:  false,
		Duration: time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	report := inspectRouteHistory(g)
	if report.Scope.ID != scope.ID {
		t.Fatalf("scope = %#v, want %#v", report.Scope, scope)
	}
	if report.Routes[historyRouteWebRTC].Attempts != 1 {
		t.Fatalf("current routes = %#v", report.Routes)
	}
	if _, ok := report.Routes[historyRouteDirect]; ok {
		t.Fatalf("other network route leaked into report: %#v", report.Routes)
	}
}

func TestInspectRouteHistoryUsesV1LegacyFallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "route-history.json")
	if err := os.WriteFile(path, []byte(`{
  "version": 1,
  "routes": {
    "direct": {
      "attempts": 3,
      "failures": 3,
      "consecutive_failures": 3
    }
  }
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	report := inspectRouteHistory(&globalOptions{RouteHistory: path})
	if !report.Legacy || report.Routes[historyRouteDirect].Attempts != 3 {
		t.Fatalf("legacy report = %#v", report)
	}
}

func TestRouteHistoryRepairsMissingRoutesMap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "route-history.json")
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recordRouteObservation(path, routeObservation{
		Kind:     historyRouteRelay,
		Success:  true,
		Duration: time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	history, err := loadRouteHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := history.Profiles[defaultRouteHistoryScope].Routes[historyRouteRelay]
	if entry.Attempts != 1 || entry.Successes != 1 {
		t.Fatalf("entry = %#v", entry)
	}
}

func TestRouteHistorySeparatesNetworkScopes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "route-history.json")
	for i := 0; i < directHistoryFailureThreshold; i++ {
		if err := recordRouteObservation(path, routeObservation{
			Kind:     historyRouteDirect,
			ScopeID:  "home-network",
			Success:  false,
			Duration: time.Second,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := recordRouteObservation(path, routeObservation{
		Kind:     historyRouteDirect,
		ScopeID:  "mobile-hotspot",
		Success:  true,
		Duration: time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	history, err := loadRouteHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	home := summarizeRouteEntries(history.Profiles["home-network"].Routes)
	mobile := summarizeRouteEntries(history.Profiles["mobile-hotspot"].Routes)
	if !shouldDeferDirectFromHistory(doctorHistoryReport{
		Enabled: true,
		Routes:  home,
	}, time.Now()) {
		t.Fatal("home-network failures did not defer direct")
	}
	if shouldDeferDirectFromHistory(doctorHistoryReport{
		Enabled: true,
		Routes:  mobile,
	}, time.Now()) {
		t.Fatal("home-network failures leaked into mobile-hotspot")
	}
}

func TestRouteHistoryPersistsAndRestoresPhysicalPathWeights(t *testing.T) {
	path := filepath.Join(t.TempDir(), "route-history.json")
	g := &globalOptions{RouteHistory: path, Relay: "127.0.0.1:19090"}
	scope := detectRouteNetworkScope(g)
	if err := recordRouteObservation(path, routeObservation{
		Kind:     historyRouteRelay,
		ScopeID:  scope.ID,
		Success:  true,
		Duration: time.Second,
		Paths: []transport.PhysicalPathStats{
			{Connection: 1, SentBytes: 1 << 20, SentChunks: 16, SendNanos: int64(time.Second)},
			{Connection: 2, SentBytes: 3 << 20, SentChunks: 48, SendNanos: int64(time.Second)},
		},
	}); err != nil {
		t.Fatal(err)
	}
	history, err := loadRouteHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	entry := history.Profiles[scope.ID].Routes[historyRouteRelay]
	if len(entry.Paths) != 2 || entry.Paths["1"].Samples != 1 || entry.Paths["2"].SentBytes != 3<<20 {
		t.Fatalf("path history = %#v", entry.Paths)
	}
	summary := summarizeRouteEntries(map[string]routeHistoryEntry{historyRouteRelay: entry})[historyRouteRelay]
	if len(summary.Paths) != 2 || summary.Paths[0].Weight != 0.5 || summary.Paths[1].Weight != 1.5 {
		t.Fatalf("path summary = %#v", summary.Paths)
	}
	weights := routePathWeights(g, historyRouteRelay, 4)
	if len(weights) != 4 || weights[0] != 1 || weights[1] != 0.5 || weights[2] != 1.5 || weights[3] != 1 {
		t.Fatalf("restored weights = %#v", weights)
	}
	reduced := routePathWeights(g, historyRouteRelay, 2)
	if len(reduced) != 2 || reduced[1] != 1 {
		t.Fatalf("reduced connection weights = %#v", reduced)
	}
}

func TestRouteHistoryMigratesV1IntoCurrentScope(t *testing.T) {
	path := filepath.Join(t.TempDir(), "route-history.json")
	if err := os.WriteFile(path, []byte(`{
  "version": 1,
  "routes": {
    "relay": {
      "attempts": 2,
      "successes": 2
    }
  }
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := recordRouteObservation(path, routeObservation{
		Kind:     historyRouteRelay,
		ScopeID:  "current-network",
		Success:  false,
		Duration: time.Second,
	}); err != nil {
		t.Fatal(err)
	}
	history, err := loadRouteHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	if history.Version != routeHistoryVersion || history.Routes != nil {
		t.Fatalf("history was not migrated: %#v", history)
	}
	entry := history.Profiles["current-network"].Routes[historyRouteRelay]
	if entry.Attempts != 3 || entry.Successes != 2 || entry.Failures != 1 {
		t.Fatalf("migrated entry = %#v", entry)
	}
}

func TestRouteNetworkScopeIsHashedAndNetworkSpecific(t *testing.T) {
	home := makeRouteNetworkScope("en0", net.ParseIP("192.168.1.24"), "test")
	again := makeRouteNetworkScope("en0", net.ParseIP("192.168.1.24"), "test")
	hotspot := makeRouteNetworkScope("en0", net.ParseIP("172.20.10.4"), "test")
	if home.ID != again.ID {
		t.Fatalf("scope is not deterministic: %q != %q", home.ID, again.ID)
	}
	if home.ID == hotspot.ID {
		t.Fatal("different local networks produced the same scope")
	}
	if strings.Contains(home.ID, "192.168") || len(home.ID) != 16 {
		t.Fatalf("scope ID exposes address or has wrong shape: %q", home.ID)
	}
	ipv6A := makeRouteNetworkScope("en0", net.ParseIP("2001:db8:1:2::1234"), "test")
	ipv6B := makeRouteNetworkScope("en0", net.ParseIP("2001:db8:1:2::5678"), "test")
	if ipv6A.ID != ipv6B.ID {
		t.Fatal("IPv6 privacy addresses in the same /64 produced different scopes")
	}
}

func TestRouteHistoryPrunesOldProfiles(t *testing.T) {
	history := newRouteHistory()
	for i := 0; i < maxRouteHistoryProfiles+3; i++ {
		id := fmt.Sprintf("scope-%02d", i)
		history.Profiles[id] = routeHistoryProfile{
			LastSeen: int64(i),
			Routes:   map[string]routeHistoryEntry{},
		}
	}
	pruneRouteHistoryProfiles(&history, "scope-00")
	if len(history.Profiles) != maxRouteHistoryProfiles {
		t.Fatalf("profile count = %d, want %d", len(history.Profiles), maxRouteHistoryProfiles)
	}
	if _, ok := history.Profiles["scope-00"]; !ok {
		t.Fatal("active profile was pruned")
	}
	if _, ok := history.Profiles["scope-01"]; ok {
		t.Fatal("oldest inactive profile was retained")
	}
}

func TestDirectHistoryDeferralUsesFailureThresholdAndCooldown(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	report := doctorHistoryReport{
		Enabled: true,
		Routes: map[string]routeHistorySummary{
			historyRouteDirect: {
				ConsecutiveFailures: directHistoryFailureThreshold,
				LastFailure:         now.Add(-time.Minute).UnixMilli(),
			},
		},
	}
	if !shouldDeferDirectFromHistory(report, now) {
		t.Fatal("recent consecutive failures did not defer direct")
	}
	report.Routes[historyRouteDirect] = routeHistorySummary{
		ConsecutiveFailures: directHistoryFailureThreshold - 1,
		LastFailure:         now.Add(-time.Minute).UnixMilli(),
	}
	if shouldDeferDirectFromHistory(report, now) {
		t.Fatal("failure count below threshold deferred direct")
	}
	report.Routes[historyRouteDirect] = routeHistorySummary{
		ConsecutiveFailures: directHistoryFailureThreshold,
		LastFailure:         now.Add(-directHistoryCooldown).UnixMilli(),
	}
	if shouldDeferDirectFromHistory(report, now) {
		t.Fatal("expired cooldown deferred direct")
	}
}

func TestRouteChoiceRequiresPeerCapability(t *testing.T) {
	local := relay.JoinOptions{
		Capabilities:     []string{relay.CapabilityRouteChoiceV1},
		DirectPreference: relay.DirectPreferenceRelay,
	}
	legacyPeer := relay.JoinResult{}
	if !peersShouldAttemptDirect(local, legacyPeer) {
		t.Fatal("missing peer capability must preserve legacy direct attempt")
	}
	newPeer := relay.JoinResult{
		PeerCapabilities:     []string{relay.CapabilityRouteChoiceV1},
		PeerDirectPreference: relay.DirectPreferencePrefer,
	}
	if peersShouldAttemptDirect(local, newPeer) {
		t.Fatal("negotiated local relay preference did not defer direct")
	}
	local.DirectPreference = relay.DirectPreferencePrefer
	if !peersShouldAttemptDirect(local, newPeer) {
		t.Fatal("two direct preferences did not attempt direct")
	}
	newPeer.PeerDirectPreference = relay.DirectPreferenceRelay
	if peersShouldAttemptDirect(local, newPeer) {
		t.Fatal("negotiated peer relay preference did not defer direct")
	}
}
