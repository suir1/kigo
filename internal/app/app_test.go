package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/suir1/kigo/internal/advisor"
	"github.com/suir1/kigo/internal/discovery"
	"github.com/suir1/kigo/internal/netprobe"
	"github.com/suir1/kigo/internal/transport"
)

func TestIPFromAddrParsesIPNet(t *testing.T) {
	_, ipNet, err := net.ParseCIDR("192.168.1.12/24")
	if err != nil {
		t.Fatal(err)
	}
	got := ipFromAddr(ipNet)
	if got.String() != "192.168.1.0" {
		t.Fatalf("got %s", got)
	}
}

func TestAdvertisedDirectAddrUsesExplicitAdvertise(t *testing.T) {
	g := &globalOptions{DirectAdvertise: "10.0.0.2:4444"}
	got := advertisedDirectAddr(g, mustResolveTCPAddr(t, "0.0.0.0:1234"))
	if got != "10.0.0.2:4444" {
		t.Fatalf("got %q", got)
	}
}

func TestAdvertisedDirectAddrKeepsConcreteHost(t *testing.T) {
	g := &globalOptions{}
	got := advertisedDirectAddr(g, mustResolveTCPAddr(t, "127.0.0.1:1234"))
	if got != "127.0.0.1:1234" {
		t.Fatalf("got %q", got)
	}
}

func TestAPIURLBuildsEndpointFromBase(t *testing.T) {
	got, err := apiURL("http://127.0.0.1:8080/base?x=1#frag", "/api/ice")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://127.0.0.1:8080/base/api/ice" {
		t.Fatalf("got %q", got)
	}
}

func TestAPIURLAddsHTTPForBareHost(t *testing.T) {
	got, err := apiURL("127.0.0.1:8080", "/api/ice")
	if err != nil {
		t.Fatal(err)
	}
	if got != "http://127.0.0.1:8080/api/ice" {
		t.Fatalf("got %q", got)
	}
}

func TestFetchSignalJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_ = json.NewEncoder(w).Encode(map[string]string{"value": "ready"})
		case "/unavailable":
			http.Error(w, "offline", http.StatusServiceUnavailable)
		case "/oversized":
			_, _ = io.WriteString(w, `{"value":"`+strings.Repeat("x", 1<<20)+`"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	var body struct {
		Value string `json:"value"`
	}
	endpoint, latency, err := fetchSignalJSON(context.Background(), server.URL, "/ok", nil, &body)
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != server.URL+"/ok" || latency <= 0 || body.Value != "ready" {
		t.Fatalf("endpoint=%q latency=%d body=%#v", endpoint, latency, body)
	}

	endpoint, _, err = fetchSignalJSON(context.Background(), server.URL, "/unavailable", nil, &body)
	if err == nil || endpoint != server.URL+"/unavailable" || !strings.Contains(err.Error(), "503 Service Unavailable") {
		t.Fatalf("endpoint=%q err=%v", endpoint, err)
	}
	if _, _, err := fetchSignalJSON(context.Background(), server.URL, "/oversized", nil, &body); err == nil {
		t.Fatal("oversized signaling JSON was accepted")
	}
}

func TestPlanNativeRouteUsesWebRTCWithoutRelay(t *testing.T) {
	plan := planNativeRoute(&globalOptions{})
	if plan.Kind != routeWebRTC {
		t.Fatalf("kind = %q", plan.Kind)
	}
	if plan.Primary != "WebRTC DataChannel" {
		t.Fatalf("primary = %q", plan.Primary)
	}
}

func TestRootCommandRegistersNoteCommands(t *testing.T) {
	root := NewRootCommand()
	command, _, err := root.Find([]string{"note", "host"})
	if err != nil {
		t.Fatal(err)
	}
	if command == nil || command.Use != "host" {
		t.Fatalf("note host command = %#v", command)
	}
	command, _, err = root.Find([]string{"note", "join"})
	if err != nil {
		t.Fatal(err)
	}
	if command == nil || command.Use != "join <code>" {
		t.Fatalf("note join command = %#v", command)
	}
	for _, path := range [][]string{
		{"send"},
		{"text", "send"},
		{"note", "host"},
	} {
		command, _, err := root.Find(path)
		if err != nil {
			t.Fatal(err)
		}
		if command.Flags().Lookup("no-qrcode") == nil {
			t.Fatalf("%v command is missing --no-qrcode", path)
		}
	}
}

func TestPrintNoteShareTarget(t *testing.T) {
	var output bytes.Buffer
	printNoteShareTarget(&output, &globalOptions{
		Transport: transportModeAuto,
		WebURL:    "https://kigo.example/",
	}, "K7M9Q2", "main")
	if got, want := output.String(), "Link: https://kigo.example/#n=K7M9Q2\n"; got != want {
		t.Fatalf("note share target = %q, want %q", got, want)
	}

	output.Reset()
	printNoteShareTarget(&output, &globalOptions{
		Transport: transportModeNative,
		WebURL:    "https://kigo.example",
	}, "K7M9Q2", "main")
	if output.Len() != 0 {
		t.Fatalf("forced native note printed browser link %q", output.String())
	}

	printNoteShareTarget(&output, &globalOptions{
		Transport: transportModeAuto,
		WebURL:    "https://kigo.example",
	}, "K7M9Q2", "scratch")
	if got, want := output.String(), "Link: https://kigo.example/#n=K7M9Q2&p=scratch\n"; got != want {
		t.Fatalf("custom-pad note link = %q, want %q", got, want)
	}
}

func TestQRCodeTargetsPreferBrowserLinks(t *testing.T) {
	auto := &globalOptions{
		Transport: transportModeAuto,
		WebURL:    "https://kigo.example/",
	}
	if got := transferQRCodeTarget(auto, "K7M9Q2"); got != "https://kigo.example/#c=K7M9Q2" {
		t.Fatalf("transfer QR target = %q", got)
	}
	if got := noteQRCodeTarget(auto, "K7M9Q2", "main"); got != "https://kigo.example/#n=K7M9Q2" {
		t.Fatalf("note QR target = %q", got)
	}
	if got := noteQRCodeTarget(auto, "K7M9Q2", "scratch"); got != "https://kigo.example/#n=K7M9Q2&p=scratch" {
		t.Fatalf("custom-pad note QR target = %q", got)
	}

	native := &globalOptions{
		Transport: transportModeNative,
		WebURL:    "https://kigo.example",
	}
	if got := transferQRCodeTarget(native, "K7M9Q2"); got != "K7M9Q2" {
		t.Fatalf("native transfer QR target = %q", got)
	}
	if got := noteQRCodeTarget(native, "K7M9Q2", "main"); got != "K7M9Q2" {
		t.Fatalf("native note QR target = %q", got)
	}
}

func TestPrintQRCodeSkipsNonTerminalWriter(t *testing.T) {
	var output bytes.Buffer
	printQRCodeIfTerminal(&output, "K7M9Q2", true)
	if output.Len() != 0 {
		t.Fatalf("non-terminal QR output = %q", output.String())
	}
}

func TestPlanNativeRouteUsesSignalingDirect(t *testing.T) {
	plan := planNativeRoute(&globalOptions{SignalDirect: true, Relay: "127.0.0.1:9000"})
	if plan.Kind != routeSignalDirect {
		t.Fatalf("kind = %q", plan.Kind)
	}
	if plan.Primary != "direct TCP via signaling rendezvous" {
		t.Fatalf("primary = %q", plan.Primary)
	}
	if plan.Fallback != "native TCP relay" {
		t.Fatalf("fallback = %q", plan.Fallback)
	}
}

func TestPlanNativeRouteUsesRelayOnlyWhenDirectDisabled(t *testing.T) {
	plan := planNativeRoute(&globalOptions{Relay: "127.0.0.1:9000", NoDirect: true})
	if plan.Kind != routeRelayOnly {
		t.Fatalf("kind = %q", plan.Kind)
	}
	if plan.Fallback != "none" {
		t.Fatalf("fallback = %q", plan.Fallback)
	}
}

func TestPlanNativeRouteUsesRelayOnlyWithProxy(t *testing.T) {
	plan := planNativeRoute(&globalOptions{
		Relay: "relay.example:9000",
		Proxy: "http://proxy.example:8080",
	})
	if plan.Kind != routeRelayOnly {
		t.Fatalf("kind = %q", plan.Kind)
	}
	if plan.Primary != "native TCP relay" || plan.Fallback != "none" {
		t.Fatalf("plan = %#v", plan)
	}
}

func TestPlanNativeRouteUsesDirectWithRelayFallback(t *testing.T) {
	plan := planNativeRoute(&globalOptions{Relay: "127.0.0.1:9000"})
	if plan.Kind != routeDirectRelayFallback {
		t.Fatalf("kind = %q", plan.Kind)
	}
	if plan.Fallback != "native TCP relay" {
		t.Fatalf("fallback = %q", plan.Fallback)
	}
}

func TestPlanNativeRouteUsesLANRelayWhenLocalRequested(t *testing.T) {
	plan := planNativeRoute(&globalOptions{Local: true})
	if plan.Kind != routeDirectRelayFallback {
		t.Fatalf("kind = %q", plan.Kind)
	}
	if plan.Fallback != "sender-hosted or discovered LAN relay" {
		t.Fatalf("fallback = %q", plan.Fallback)
	}
}

func TestInspectLANFindsAnnouncedRelay(t *testing.T) {
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 0})
	if err != nil {
		t.Fatal(err)
	}
	discoveryAddr := probe.LocalAddr().String()
	if err := probe.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = discovery.Announce(ctx, discoveryAddr, 19090)
	}()
	time.Sleep(20 * time.Millisecond)

	report := inspectLAN(context.Background(), &globalOptions{
		Local:         true,
		DiscoveryAddr: discoveryAddr,
		LANTimeout:    500 * time.Millisecond,
	})
	if !report.OK || len(report.Relays) != 1 || report.Relays[0] != "127.0.0.1:19090" {
		t.Fatalf("report = %#v", report)
	}
}

func TestFormatErrorMapsUserFacingFailures(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{context.Canceled, "canceled"},
		{context.DeadlineExceeded, "timed out"},
		{pairTimeoutError{timeout: 5 * time.Minute}, "pairing timed out after 5m0s"},
		{io.EOF, "connection closed"},
		{transport.ErrClosed, "connection closed"},
		{errors.New("relay password required"), "relay password rejected"},
		{errors.New("relay room expired"), "pairing room expired"},
		{errors.New("room is locked"), "pairing code is already in use"},
		{errors.New("dial tcp 127.0.0.1:8080: connect: connection refused"), "connection refused"},
		{errors.New("write tcp: broken pipe"), "peer disconnected"},
		{errors.New("expected complete, got \"chunk\""), "incompatible transfer protocol"},
	}
	for _, tt := range tests {
		if got := FormatError(tt.err); !strings.Contains(got, tt.want) {
			t.Fatalf("FormatError(%q) = %q, want substring %q", tt.err, got, tt.want)
		}
	}
}

func TestSummarizeICECountsSTUNAndTURNAuth(t *testing.T) {
	got := summarizeICE(iceConfig{ICEServers: []struct {
		URLs       []string `json:"urls"`
		Username   string   `json:"username"`
		Credential string   `json:"credential"`
	}{
		{URLs: []string{"stun:stun.example:3478"}},
		{URLs: []string{"turn:turn.example:3478"}, Username: "user", Credential: "pass"},
		{URLs: []string{"turns:turns.example:5349"}},
	}})
	if got.Total != 3 || got.STUN != 1 || got.TURN != 2 || got.TURNWithAuth != 1 || got.TURNWithoutAuth != 1 {
		t.Fatalf("summary = %#v", got)
	}
}

func TestICEFallbackText(t *testing.T) {
	if got := iceFallbackText(iceSummary{TURN: 1}); got != "TURN fallback available" {
		t.Fatalf("turn text = %q", got)
	}
	if got := iceFallbackText(iceSummary{STUN: 1}); got != "STUN only" {
		t.Fatalf("stun text = %q", got)
	}
	if got := iceFallbackText(iceSummary{}); got != "no ICE servers advertised" {
		t.Fatalf("empty text = %q", got)
	}
}

func TestPlanRouteCandidatesPrefersDirectRelayFallback(t *testing.T) {
	routes := planRouteCandidates(
		doctorSignalReport{OK: true, ICE: iceSummary{STUN: 1, TURN: 1}},
		doctorRelayReport{Configured: true, OK: true, Addr: "127.0.0.1:9000"},
		doctorDirectReport{Enabled: true, OK: true, Advertise: "192.168.1.2:4444"},
		netprobe.STUNReport{},
		&globalOptions{Relay: "127.0.0.1:9000"},
	)
	route, ok := primaryRoute(routes, "native-native")
	if !ok {
		t.Fatal("missing native-native primary route")
	}
	if route.Kind != routeDirectRelayFallback || !route.Available || route.Score <= 0 {
		t.Fatalf("route = %#v", route)
	}
	if route.Fallback != "native TCP relay" {
		t.Fatalf("fallback = %q", route.Fallback)
	}
}

func TestPlanRouteCandidatesPrefersSignalingDirectWhenAdvertised(t *testing.T) {
	routes := planRouteCandidates(
		doctorSignalReport{
			OK:            true,
			LatencyMillis: 15,
			ICE:           iceSummary{STUN: 1, TURN: 1},
			Capabilities:  []string{"direct-rendezvous-v1"},
		},
		doctorRelayReport{Configured: true, OK: true, Addr: "127.0.0.1:9000"},
		doctorDirectReport{Enabled: true, OK: true, Advertise: "192.168.1.2:4444"},
		netprobe.STUNReport{},
		&globalOptions{Relay: "127.0.0.1:9000"},
	)
	route, ok := primaryRoute(routes, "native-native")
	if !ok {
		t.Fatal("missing native-native primary route")
	}
	if route.Kind != routeSignalDirect || !route.Available {
		t.Fatalf("route = %#v", route)
	}
	if route.Fallback != "native TCP relay" {
		t.Fatalf("fallback = %q", route.Fallback)
	}
}

func TestPlanRouteCandidatesWarnsWithoutTURN(t *testing.T) {
	routes := planRouteCandidates(
		doctorSignalReport{OK: true, ICE: iceSummary{STUN: 1}},
		doctorRelayReport{},
		doctorDirectReport{Enabled: true, OK: true},
		netprobe.STUNReport{},
		&globalOptions{},
	)
	route, ok := primaryRoute(routes, "web-web")
	if !ok {
		t.Fatal("missing web-web primary route")
	}
	if route.Kind != routeWebRTC || !route.Available {
		t.Fatalf("route = %#v", route)
	}
	if len(route.Warnings) == 0 || !strings.Contains(strings.Join(route.Warnings, " "), "TURN") {
		t.Fatalf("warnings = %#v", route.Warnings)
	}
}

func TestLiveProbeAdjustsRouteScoreAndIsReported(t *testing.T) {
	fast := routeCandidate{Score: 70, Available: true}
	applyLiveProbe(&fast, "signaling", 25)
	if fast.Score != 74 || fast.Probe == nil || fast.Probe.LatencyMillis != 25 {
		t.Fatalf("fast probe candidate = %#v", fast)
	}
	slow := routeCandidate{Score: 70, Available: true}
	applyLiveProbe(&slow, "relay", 900)
	if slow.Score != 62 || slow.Probe == nil || slow.Probe.Kind != "relay" {
		t.Fatalf("slow probe candidate = %#v", slow)
	}
}

func TestFormatRoutePathWeightsOnlyReportsAdaptiveProfiles(t *testing.T) {
	if got := formatRoutePathWeights([]float64{1, 1, 1}); got != "" {
		t.Fatalf("default weights = %q", got)
	}
	if got := formatRoutePathWeights([]float64{1, 0.5, 1.5}); got != "p1=0.50 p2=1.50" {
		t.Fatalf("adaptive weights = %q", got)
	}
}

func TestPlanRouteCandidatesIncludesLiveProbe(t *testing.T) {
	routes := planRouteCandidates(
		doctorSignalReport{OK: true, LatencyMillis: 18, ICE: iceSummary{TURN: 1}},
		doctorRelayReport{Configured: true, OK: true, LatencyMillis: 12},
		doctorDirectReport{Enabled: true, OK: true},
		netprobe.STUNReport{},
		&globalOptions{Relay: "relay.example:9000"},
	)
	for _, route := range routes {
		if !route.Available {
			continue
		}
		if route.Probe == nil || route.Probe.LatencyMillis <= 0 {
			t.Fatalf("available route missing live probe: %#v", route)
		}
	}
}

func TestPlanRouteCandidatesAppliesNATHeuristics(t *testing.T) {
	symmetric := netprobe.STUNReport{OK: true, Class: netprobe.NATSymmetric}
	routes := planRouteCandidates(
		doctorSignalReport{
			OK:           true,
			ICE:          iceSummary{STUN: 1},
			Capabilities: []string{"direct-rendezvous-v1"},
		},
		doctorRelayReport{},
		doctorDirectReport{Enabled: true, OK: true},
		symmetric,
		&globalOptions{},
	)
	direct, ok := routeByKind(routes, routeSignalDirect)
	if !ok {
		t.Fatal("missing signaling direct route")
	}
	if direct.Score != 80 ||
		!strings.Contains(strings.Join(direct.Warnings, " "), "mapping varies") {
		t.Fatalf("direct route = %#v", direct)
	}
	webrtcRoute, ok := routeByKind(routes, routeWebRTC)
	if !ok {
		t.Fatal("missing WebRTC route")
	}
	if webrtcRoute.Pair != "native-native" ||
		webrtcRoute.Score != 60 ||
		!strings.Contains(strings.Join(webrtcRoute.Warnings, " "), "without TURN") {
		t.Fatalf("WebRTC route = %#v", webrtcRoute)
	}

	withTURN := planRouteCandidates(
		doctorSignalReport{
			OK:           true,
			ICE:          iceSummary{STUN: 1, TURN: 1},
			Capabilities: []string{"direct-rendezvous-v1"},
		},
		doctorRelayReport{},
		doctorDirectReport{Enabled: true, OK: true},
		symmetric,
		&globalOptions{},
	)
	nativeWeb, ok := primaryRoute(withTURN, "native-web")
	if !ok || nativeWeb.Score != 93 ||
		!strings.Contains(strings.Join(nativeWeb.Reasons, " "), "covered by TURN") {
		t.Fatalf("native-web route = %#v", nativeWeb)
	}
}

func TestAssessDoctorPairExplainsDirectRelayRoute(t *testing.T) {
	report := doctorReport{
		Signal: doctorSignalReport{OK: true, ICE: iceSummary{TURN: 1}},
		Relay:  doctorRelayReport{Configured: true, OK: true},
		Direct: doctorDirectReport{Enabled: true, OK: true},
		Routes: []routeCandidate{{
			Pair:      "native-native",
			Kind:      routeDirectRelayFallback,
			Name:      "direct TCP via relay rendezvous",
			Score:     92,
			Available: true,
			Primary:   true,
			Fallback:  "native TCP relay",
		}},
	}

	assessment := assessDoctorPair(report, "native-native")
	if assessment.Recommendation != "direct_preferred_with_relay_fallback" {
		t.Fatalf("recommendation = %q", assessment.Recommendation)
	}
	if assessment.RouteResult.Path != "direct_or_relay" ||
		!assessment.RouteResult.DirectAttempted ||
		assessment.RouteResult.DataRelayRequired ||
		!assessment.RouteResult.RendezvousRelayRequired {
		t.Fatalf("route hint = %#v", assessment.RouteResult)
	}
}

func TestAssessDoctorPairRecommendsTURNForBrowserRoute(t *testing.T) {
	report := doctorReport{
		Signal: doctorSignalReport{OK: true, ICE: iceSummary{STUN: 2}},
		Routes: []routeCandidate{{
			Pair:      "web-web",
			Kind:      routeWebRTC,
			Name:      "WebRTC DataChannel via signaling",
			Score:     80,
			Available: true,
			Primary:   true,
			Fallback:  "STUN only",
		}},
	}

	assessment := assessDoctorPair(report, "web-web")
	if assessment.Recommendation != "configure_turn" || len(assessment.Actions) != 1 {
		t.Fatalf("assessment = %#v", assessment)
	}
	if assessment.RouteResult.Path != "webrtc" || assessment.RouteResult.DataRelayRequired {
		t.Fatalf("route hint = %#v", assessment.RouteResult)
	}
}

func TestAssessDoctorPairExplainsUnavailableRoute(t *testing.T) {
	report := doctorReport{
		Signal: doctorSignalReport{Error: "connection refused"},
		Routes: []routeCandidate{{
			Pair:    "native-web",
			Kind:    routeWebRTC,
			Name:    "WebRTC DataChannel via signaling",
			Primary: true,
		}},
	}

	assessment := assessDoctorPair(report, "native-web")
	if assessment.Recommendation != "fix_signaling" || len(assessment.Actions) == 0 {
		t.Fatalf("assessment = %#v", assessment)
	}
	if assessment.RouteResult.Path != "none" || assessment.RouteResult.Reason != "no_available_route" {
		t.Fatalf("route hint = %#v", assessment.RouteResult)
	}
}

func TestApplyAIExplanationOnlyRewritesDiagnosis(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 2 || strings.Contains(body.Messages[1].Content, "192.0.2.44") {
			t.Fatalf("AI request was not sanitized: %#v", body.Messages)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]string{"content": "Direct TCP is preferred and relay remains available."},
			}},
		})
	}))
	defer server.Close()

	report := doctorReport{
		Signal: doctorSignalReport{OK: true, BaseURL: "http://192.0.2.44", ICE: iceSummary{TURN: 1}},
		STUN:   netprobe.STUNReport{OK: true, Class: netprobe.NATCone, Local: "192.0.2.44:1234"},
		Routes: []routeCandidate{{
			Pair:      "native-native",
			Kind:      routeDirectRelayFallback,
			Name:      "direct TCP via relay rendezvous",
			Score:     92,
			Available: true,
			Primary:   true,
			Fallback:  "native TCP relay",
		}},
	}
	report.Assessment = assessDoctorPair(report, "native-native")
	originalRecommendation := report.Assessment.Recommendation
	originalHint := report.Assessment.RouteResult
	applyAIExplanation(context.Background(), &report, "native-native", advisor.Config{
		BaseURL: server.URL,
		APIKey:  "test-key",
	})

	if report.Assessment.ExplanationSource != "ai" ||
		report.Assessment.Diagnosis != "Direct TCP is preferred and relay remains available." {
		t.Fatalf("assessment = %#v", report.Assessment)
	}
	if report.Assessment.Recommendation != originalRecommendation || report.Assessment.RouteResult != originalHint {
		t.Fatalf("AI changed route decision: %#v", report.Assessment)
	}
}

func TestApplyAIExplanationFallsBackToRules(t *testing.T) {
	report := doctorReport{
		Assessment: doctorAssessment{
			Diagnosis:         "Rule diagnosis",
			ExplanationSource: "rules",
			Recommendation:    "fix_signaling",
		},
	}
	applyAIExplanation(context.Background(), &report, "native-native", advisor.Config{})
	if report.Assessment.Diagnosis != "Rule diagnosis" ||
		report.Assessment.ExplanationSource != "rules" ||
		!strings.Contains(report.Assessment.ExplanationWarning, "API key") {
		t.Fatalf("assessment = %#v", report.Assessment)
	}
}

func TestAdvisorConfigFromEnvPrefersKigoKey(t *testing.T) {
	t.Setenv("KIGO_AI_BASE_URL", "https://ai.example/v1")
	t.Setenv("KIGO_AI_MODEL", "route-model")
	t.Setenv("KIGO_AI_API_KEY", "kigo-key")
	t.Setenv("OPENAI_API_KEY", "fallback-key")
	config := advisorConfigFromEnv()
	if config.BaseURL != "https://ai.example/v1" || config.Model != "route-model" || config.APIKey != "kigo-key" {
		t.Fatalf("config = %#v", config)
	}
}

func routeByKind(routes []routeCandidate, kind routeKind) (routeCandidate, bool) {
	for _, route := range routes {
		if route.Kind == kind {
			return route, true
		}
	}
	return routeCandidate{}, false
}

func TestBuildDoctorReportSummarizesSignalAndRoutes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ice":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"iceServers": []map[string]any{
					{"urls": []string{"stun:stun.example:3478"}},
					{"urls": []string{"turn:turn.example:3478"}, "username": "u", "credential": "p"},
				},
			})
		case "/api/health":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"capabilities": []string{"direct-rendezvous-v1"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	report := buildDoctorReport(context.Background(), &globalOptions{Signal: server.URL, NoDirect: true})
	if !report.OK {
		t.Fatalf("report errors = %#v", report.Errors)
	}
	if !report.Signal.OK || report.Signal.ICE.STUN != 1 || report.Signal.ICE.TURN != 1 {
		t.Fatalf("signal = %#v", report.Signal)
	}
	if !hasCapability(report.Signal.Capabilities, "direct-rendezvous-v1") {
		t.Fatalf("signal capabilities = %#v", report.Signal.Capabilities)
	}
	if report.Signal.LatencyMillis <= 0 {
		t.Fatalf("signal latency = %d", report.Signal.LatencyMillis)
	}
	if report.Version.Version == "" || report.Version.Go == "" || report.Version.OS == "" || report.Version.Arch == "" {
		t.Fatalf("version = %#v", report.Version)
	}
	if len(report.Routes) == 0 {
		t.Fatal("expected route candidates")
	}
	if len(report.Matrix) != 3 || report.Matrix[0].Pair != "native-native" {
		t.Fatalf("matrix = %#v", report.Matrix)
	}
}

func TestBuildDoctorReportIncludesRelayFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}

	report := buildDoctorReport(context.Background(), &globalOptions{
		Signal:   "http://127.0.0.1:1",
		Relay:    addr,
		NoDirect: true,
	})
	if report.OK {
		t.Fatal("expected report to fail")
	}
	if !report.Relay.Configured || report.Relay.OK || report.Relay.Error == "" {
		t.Fatalf("relay = %#v", report.Relay)
	}
	if len(report.Errors) == 0 {
		t.Fatal("expected errors")
	}
}

func TestRouteCommandJSONReturnsCandidatesEvenWhenChecksFail(t *testing.T) {
	cmd := NewRootCommand()
	cmd.SetArgs([]string{"--signal", "http://127.0.0.1:1", "route", "--json", "--timeout", "50ms"})
	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	var body struct {
		OK     bool             `json:"ok"`
		Routes []routeCandidate `json:"routes"`
		Errors []string         `json:"errors"`
	}
	if err := json.Unmarshal([]byte(output), &body); err != nil {
		t.Fatal(err)
	}
	if body.OK {
		t.Fatal("expected route report to include failed checks")
	}
	if len(body.Routes) == 0 {
		t.Fatal("expected route candidates")
	}
	if len(body.Errors) == 0 {
		t.Fatal("expected warning errors")
	}
}

func TestRouteCommandPairFiltersCandidates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"iceServers": []map[string]any{
				{"urls": []string{"stun:stun.example:3478"}},
				{"urls": []string{"turn:turn.example:3478"}, "username": "u", "credential": "p"},
			},
		})
	}))
	defer server.Close()

	cmd := NewRootCommand()
	cmd.SetArgs([]string{"--signal", server.URL, "route", "--json", "--pair", "web-web"})
	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	var body routeReport
	if err := json.Unmarshal([]byte(output), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Routes) != 1 || body.Routes[0].Pair != "web-web" {
		t.Fatalf("routes = %#v", body.Routes)
	}
	if len(body.Matrix) != 1 || body.Matrix[0].Pair != "web-web" {
		t.Fatalf("matrix = %#v", body.Matrix)
	}
}

func TestDoctorCommandAIExplainJSON(t *testing.T) {
	signalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ice":
			_ = json.NewEncoder(w).Encode(map[string]any{"iceServers": []any{}})
		case "/api/health":
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer signalServer.Close()

	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]string{"content": "The configured browser route is available, but TURN should be added."},
			}},
		})
	}))
	defer aiServer.Close()
	t.Setenv("KIGO_AI_BASE_URL", aiServer.URL)
	t.Setenv("KIGO_AI_API_KEY", "test-key")
	t.Setenv("KIGO_AI_MODEL", "test-model")

	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"--signal", signalServer.URL,
		"--no-direct",
		"--no-route-history",
		"doctor",
		"--json",
		"--ai-explain",
		"--timeout", "1s",
		"--ai-timeout", "1s",
	})
	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	var report doctorReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatal(err)
	}
	if report.Assessment.ExplanationSource != "ai" ||
		report.Assessment.Diagnosis != "The configured browser route is available, but TURN should be added." {
		t.Fatalf("assessment = %#v", report.Assessment)
	}
	if report.Assessment.Recommendation != "configure_turn" || report.Assessment.RouteResult.Path != "webrtc" {
		t.Fatalf("AI changed deterministic decision: %#v", report.Assessment)
	}
}

func TestRouteCommandAIExplainUsesSelectedPair(t *testing.T) {
	signalServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ice":
			_ = json.NewEncoder(w).Encode(map[string]any{"iceServers": []any{}})
		case "/api/health":
			_ = json.NewEncoder(w).Encode(map[string]any{"capabilities": []string{}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer signalServer.Close()

	aiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if len(body.Messages) != 2 || !strings.Contains(body.Messages[1].Content, `"pair":"web-web"`) {
			t.Fatalf("messages = %#v", body.Messages)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{
				"message": map[string]string{"content": "WebRTC is selected for both browser peers."},
			}},
		})
	}))
	defer aiServer.Close()
	t.Setenv("KIGO_AI_BASE_URL", aiServer.URL)
	t.Setenv("KIGO_AI_API_KEY", "test-key")

	cmd := NewRootCommand()
	cmd.SetArgs([]string{
		"--signal", signalServer.URL,
		"--no-direct",
		"--no-route-history",
		"route",
		"--pair", "web-web",
		"--json",
		"--ai-explain",
		"--timeout", "1s",
		"--ai-timeout", "1s",
	})
	output := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	var report routeReport
	if err := json.Unmarshal([]byte(output), &report); err != nil {
		t.Fatal(err)
	}
	if report.Assessment.ExplanationSource != "ai" ||
		report.Assessment.Diagnosis != "WebRTC is selected for both browser peers." ||
		report.Assessment.Recommendation != "configure_turn" {
		t.Fatalf("assessment = %#v", report.Assessment)
	}
}

func TestDoctorDirectHonorsNoDirect(t *testing.T) {
	g := &globalOptions{NoDirect: true, DirectListen: "bad listen address"}
	if err := doctorDirect(g); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorRelayFailsClosedPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	if err := ln.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := doctorRelay(ctx, addr); err == nil {
		t.Fatal("expected closed relay port to fail")
	}
}

func TestFlagEnvStringUsesEnvWhenFlagIsDefault(t *testing.T) {
	t.Setenv("KIGO_TURN_PASS", "from-env")
	cmd := &cobra.Command{}
	cmd.Flags().String("turn-pass", "default-pass", "")

	got := flagEnvString(cmd, "turn-pass", "default-pass", "KIGO_TURN_PASS")
	if got != "from-env" {
		t.Fatalf("got %q", got)
	}
}

func TestRootRejectsInvalidProxyURL(t *testing.T) {
	command := NewRootCommand()
	command.SetArgs([]string{"--proxy", "https://proxy.example", "version"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "unsupported proxy scheme") {
		t.Fatalf("error = %v", err)
	}
}

func TestRootRejectsNonPositivePairTimeout(t *testing.T) {
	command := NewRootCommand()
	command.SetArgs([]string{"--pair-timeout", "0s", "version"})
	err := command.Execute()
	if err == nil || !strings.Contains(err.Error(), "pair timeout must be positive") {
		t.Fatalf("error = %v", err)
	}
}

func TestSenderCommandsExposeCustomCodeFlag(t *testing.T) {
	root := NewRootCommand()
	for _, args := range [][]string{{"send"}, {"text", "send"}, {"note", "host"}} {
		command, _, err := root.Find(args)
		if err != nil {
			t.Fatalf("find %v: %v", args, err)
		}
		if command.Flags().Lookup("code") == nil {
			t.Fatalf("%v is missing --code", args)
		}
	}
}

func TestFlagEnvStringKeepsExplicitFlag(t *testing.T) {
	t.Setenv("KIGO_TURN_PASS", "from-env")
	cmd := &cobra.Command{}
	var value string
	cmd.Flags().StringVar(&value, "turn-pass", "default-pass", "")
	if err := cmd.Flags().Set("turn-pass", "from-flag"); err != nil {
		t.Fatal(err)
	}

	got := flagEnvString(cmd, "turn-pass", value, "KIGO_TURN_PASS")
	if got != "from-flag" {
		t.Fatalf("got %q", got)
	}
}

func TestFlagEnvDurationUsesEnvWhenFlagIsDefault(t *testing.T) {
	t.Setenv("KIGO_RELAY_ROOM_TTL", "45s")
	cmd := &cobra.Command{}
	cmd.Flags().Duration("room-ttl", time.Minute, "")

	got, err := flagEnvDuration(cmd, "room-ttl", time.Minute, "KIGO_RELAY_ROOM_TTL")
	if err != nil {
		t.Fatal(err)
	}
	if got != 45*time.Second {
		t.Fatalf("got %s", got)
	}
}

func TestFlagEnvDurationReportsInvalidEnv(t *testing.T) {
	t.Setenv("KIGO_RELAY_ROOM_TTL", "soon")
	cmd := &cobra.Command{}
	cmd.Flags().Duration("room-ttl", time.Minute, "")

	_, err := flagEnvDuration(cmd, "room-ttl", time.Minute, "KIGO_RELAY_ROOM_TTL")
	if err == nil || !strings.Contains(err.Error(), "KIGO_RELAY_ROOM_TTL") {
		t.Fatalf("err = %v", err)
	}
}

func TestFlagEnvIntUsesEnvWhenFlagIsDefault(t *testing.T) {
	t.Setenv("KIGO_TURN_MAX_ALLOCATIONS", "256")
	cmd := &cobra.Command{}
	cmd.Flags().Int("turn-max-allocations", 1024, "")

	got, err := flagEnvInt(cmd, "turn-max-allocations", 1024, "KIGO_TURN_MAX_ALLOCATIONS")
	if err != nil {
		t.Fatal(err)
	}
	if got != 256 {
		t.Fatalf("got %d", got)
	}
}

func TestFlagEnvIntReportsInvalidEnv(t *testing.T) {
	t.Setenv("KIGO_TURN_MAX_ALLOCATIONS", "many")
	cmd := &cobra.Command{}
	cmd.Flags().Int("turn-max-allocations", 1024, "")

	_, err := flagEnvInt(cmd, "turn-max-allocations", 1024, "KIGO_TURN_MAX_ALLOCATIONS")
	if err == nil || !strings.Contains(err.Error(), "KIGO_TURN_MAX_ALLOCATIONS") {
		t.Fatalf("err = %v", err)
	}
}

func TestFlagEnvInt64UsesEnvWhenFlagIsDefault(t *testing.T) {
	t.Setenv("KIGO_TURN_MAX_EGRESS_MIB", "4096")
	cmd := &cobra.Command{}
	cmd.Flags().Int64("turn-max-egress-mib", -1, "")

	got, err := flagEnvInt64(cmd, "turn-max-egress-mib", -1, "KIGO_TURN_MAX_EGRESS_MIB")
	if err != nil {
		t.Fatal(err)
	}
	if got != 4096 {
		t.Fatalf("got %d", got)
	}
}

func TestMebibyteLimitConvertsAndValidates(t *testing.T) {
	got, err := mebibyteLimit(2048, "--turn-max-egress-mib")
	if err != nil {
		t.Fatal(err)
	}
	if got != 2<<30 {
		t.Fatalf("bytes = %d", got)
	}
	if got, err := mebibyteLimit(-1, "--turn-max-egress-mib"); err != nil || got != -1 {
		t.Fatalf("disabled limit = %d, err = %v", got, err)
	}
	for _, value := range []int64{0, -2, int64(^uint64(0)>>1)/(1<<20) + 1} {
		if _, err := mebibyteLimit(value, "--turn-max-egress-mib"); err == nil {
			t.Fatalf("limit %d was accepted", value)
		}
	}
}

func TestServeCheckConfigDoesNotBindListeners(t *testing.T) {
	for _, name := range []string{
		"KIGO_LISTEN",
		"KIGO_PUBLIC_URL",
		"KIGO_NATIVE_RELAY",
		"KIGO_NATIVE_RELAY_SECRET",
		"KIGO_NATIVE_RELAY_CREDENTIAL_TTL",
		"KIGO_SIGNAL_REQUESTS_PER_MINUTE",
		"KIGO_TLS_CERT",
		"KIGO_TLS_KEY",
		"KIGO_TRUSTED_PROXIES",
		"KIGO_TURN",
		"KIGO_TURN_LISTEN",
		"KIGO_TURN_MIN_PORT",
		"KIGO_TURN_MAX_PORT",
		"KIGO_TURN_SECRET",
	} {
		t.Setenv(name, "")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	command := NewRootCommand()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs([]string{
		"serve",
		"--listen", listener.Addr().String(),
		"--public-url", "http://kigo.example",
		"--check-config",
	})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output.String()) != "configuration valid" {
		t.Fatalf("output = %q", output.String())
	}
}

func mustResolveTCPAddr(t *testing.T, value string) *net.TCPAddr {
	t.Helper()
	addr, err := net.ResolveTCPAddr("tcp", value)
	if err != nil {
		t.Fatal(err)
	}
	return addr
}

func TestCaptureStdoutHandlesOutputLargerThanPipeBuffer(t *testing.T) {
	payload := strings.Repeat("x", 1024*1024)
	output := captureStdout(t, func() {
		_, _ = io.WriteString(os.Stdout, payload)
	})
	if output != payload {
		t.Fatalf("captured %d bytes, want %d", len(output), len(payload))
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = old
		_ = w.Close()
		_ = r.Close()
	}()
	type readResult struct {
		data []byte
		err  error
	}
	result := make(chan readResult, 1)
	go func() {
		data, err := io.ReadAll(r)
		result <- readResult{data: data, err: err}
	}()
	fn()
	os.Stdout = old
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	captured := <-result
	if captured.err != nil {
		t.Fatal(captured.err)
	}
	return string(captured.data)
}
