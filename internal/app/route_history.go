package app

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/suir1/kigo/internal/localstate"
	"github.com/suir1/kigo/internal/transport"
)

const routeHistoryVersion = 2
const maxRouteHistoryBytes = 1 << 20
const maxRouteHistoryProfiles = 16
const directHistoryFailureThreshold = 3
const directHistoryCooldown = 30 * time.Minute
const defaultRouteHistoryScope = "unscoped"

const (
	historyRouteDirect = "direct"
	historyRouteRelay  = "relay"
	historyRouteWebRTC = "webrtc"
)

var routeHistoryMu sync.Mutex

type routeHistoryFile struct {
	Version  int                            `json:"version"`
	Profiles map[string]routeHistoryProfile `json:"profiles,omitempty"`
	Routes   map[string]routeHistoryEntry   `json:"routes,omitempty"`
}

type routeHistoryProfile struct {
	LastSeen int64                        `json:"last_seen"`
	Routes   map[string]routeHistoryEntry `json:"routes"`
}

type routeHistoryEntry struct {
	Attempts            int                              `json:"attempts"`
	Successes           int                              `json:"successes"`
	Failures            int                              `json:"failures"`
	ConsecutiveFailures int                              `json:"consecutive_failures"`
	SentBytes           int64                            `json:"sent_bytes"`
	ReceivedBytes       int64                            `json:"received_bytes"`
	DurationMillis      int64                            `json:"duration_ms"`
	EWMABytesPerSecond  float64                          `json:"ewma_bytes_per_second"`
	LastSuccess         int64                            `json:"last_success,omitempty"`
	LastFailure         int64                            `json:"last_failure,omitempty"`
	Paths               map[string]routePathHistoryEntry `json:"paths,omitempty"`
}

type routePathHistoryEntry struct {
	Samples            int     `json:"samples"`
	SentBytes          int64   `json:"sent_bytes"`
	SendNanos          int64   `json:"send_nanos"`
	EWMABytesPerSecond float64 `json:"ewma_bytes_per_second"`
}

type routeHistorySummary struct {
	Attempts            int                       `json:"attempts"`
	Successes           int                       `json:"successes"`
	Failures            int                       `json:"failures"`
	SuccessRate         float64                   `json:"success_rate"`
	ConsecutiveFailures int                       `json:"consecutive_failures"`
	AverageDurationMS   int64                     `json:"average_duration_ms"`
	EWMABytesPerSecond  float64                   `json:"ewma_bytes_per_second"`
	LastSuccess         int64                     `json:"last_success,omitempty"`
	LastFailure         int64                     `json:"last_failure,omitempty"`
	Paths               []routePathHistorySummary `json:"paths,omitempty"`
}

type routePathHistorySummary struct {
	Connection         int     `json:"connection"`
	Samples            int     `json:"samples"`
	SentBytes          int64   `json:"sent_bytes"`
	EWMABytesPerSecond float64 `json:"ewma_bytes_per_second"`
	Weight             float64 `json:"weight"`
}

type doctorHistoryReport struct {
	Enabled bool                           `json:"enabled"`
	Path    string                         `json:"path,omitempty"`
	Scope   routeNetworkScope              `json:"scope"`
	Legacy  bool                           `json:"legacy_fallback,omitempty"`
	Routes  map[string]routeHistorySummary `json:"routes,omitempty"`
	Error   string                         `json:"error,omitempty"`
}

type routeObservation struct {
	Kind          string
	ScopeID       string
	Success       bool
	SentBytes     int64
	ReceivedBytes int64
	Duration      time.Duration
	Paths         []transport.PhysicalPathStats
}

type routeNetworkScope struct {
	ID        string `json:"id"`
	Interface string `json:"interface,omitempty"`
	Family    string `json:"family,omitempty"`
	Source    string `json:"source,omitempty"`
}

func defaultRouteHistoryPath() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil || cacheDir == "" {
		return ""
	}
	return filepath.Join(cacheDir, "kigo", "route-history.json")
}

func recordObservedRoute(g *globalOptions, t transport.Transport, success bool, duration time.Duration) {
	if g == nil || g.NoRouteHistory || g.RouteHistory == "" {
		return
	}
	stats, ok := transport.SnapshotRouteStats(t)
	if !ok || !validHistoryRoute(stats.Kind) {
		return
	}
	scope := detectRouteNetworkScope(g)
	_ = recordRouteObservation(g.RouteHistory, routeObservation{
		Kind:          stats.Kind,
		ScopeID:       scope.ID,
		Success:       success,
		SentBytes:     stats.SentBytes,
		ReceivedBytes: stats.ReceivedBytes,
		Duration:      duration,
		Paths:         stats.Paths,
	})
}

func recordRouteFailure(g *globalOptions, kind string, duration time.Duration) {
	if g == nil || g.NoRouteHistory || g.RouteHistory == "" || !validHistoryRoute(kind) {
		return
	}
	scope := detectRouteNetworkScope(g)
	_ = recordRouteObservation(g.RouteHistory, routeObservation{
		Kind:     kind,
		ScopeID:  scope.ID,
		Success:  false,
		Duration: duration,
	})
}

func recordRouteObservation(path string, observation routeObservation) error {
	if path == "" || !validHistoryRoute(observation.Kind) {
		return nil
	}
	routeHistoryMu.Lock()
	defer routeHistoryMu.Unlock()
	return localstate.WithFileLock(path, func() error {
		history, err := loadRouteHistory(path)
		if err != nil {
			history = newRouteHistory()
		}
		scopeID := observation.ScopeID
		if scopeID == "" {
			scopeID = defaultRouteHistoryScope
		}
		history = migrateRouteHistory(history, scopeID)
		profile := history.Profiles[scopeID]
		if profile.Routes == nil {
			profile.Routes = map[string]routeHistoryEntry{}
		}
		entry := profile.Routes[observation.Kind]
		entry.Attempts++
		entry.SentBytes += max(observation.SentBytes, 0)
		entry.ReceivedBytes += max(observation.ReceivedBytes, 0)
		if observation.Duration > 0 {
			entry.DurationMillis += observation.Duration.Milliseconds()
		}
		now := time.Now().UnixMilli()
		if observation.Success {
			entry.Successes++
			entry.ConsecutiveFailures = 0
			entry.LastSuccess = now
			totalBytes := observation.SentBytes + observation.ReceivedBytes
			if observation.Duration > 0 && totalBytes > 0 {
				rate := float64(totalBytes) / observation.Duration.Seconds()
				if entry.EWMABytesPerSecond == 0 {
					entry.EWMABytesPerSecond = rate
				} else {
					entry.EWMABytesPerSecond = entry.EWMABytesPerSecond*0.75 + rate*0.25
				}
			}
		} else {
			entry.Failures++
			entry.ConsecutiveFailures++
			entry.LastFailure = now
		}
		if len(observation.Paths) > 0 {
			if entry.Paths == nil {
				entry.Paths = map[string]routePathHistoryEntry{}
			}
			for _, pathStats := range observation.Paths {
				if pathStats.Connection <= 0 || pathStats.SentBytes <= 0 || pathStats.SendNanos <= 0 {
					continue
				}
				key := strconv.Itoa(pathStats.Connection)
				pathEntry := entry.Paths[key]
				pathEntry.Samples++
				pathEntry.SentBytes += pathStats.SentBytes
				pathEntry.SendNanos += pathStats.SendNanos
				rate := float64(pathStats.SentBytes) / (float64(pathStats.SendNanos) / float64(time.Second))
				if pathEntry.EWMABytesPerSecond == 0 {
					pathEntry.EWMABytesPerSecond = rate
				} else {
					pathEntry.EWMABytesPerSecond = pathEntry.EWMABytesPerSecond*0.75 + rate*0.25
				}
				entry.Paths[key] = pathEntry
			}
		}
		profile.Routes[observation.Kind] = entry
		profile.LastSeen = now
		history.Profiles[scopeID] = profile
		pruneRouteHistoryProfiles(&history, scopeID)
		return localstate.WriteJSON(path, history)
	})
}

func inspectRouteHistory(g *globalOptions) doctorHistoryReport {
	report := doctorHistoryReport{}
	if g == nil || g.NoRouteHistory || g.RouteHistory == "" {
		return report
	}
	report.Enabled = true
	report.Path = g.RouteHistory
	report.Scope = detectRouteNetworkScope(g)
	history, err := loadRouteHistory(g.RouteHistory)
	if errors.Is(err, os.ErrNotExist) {
		return report
	}
	if err != nil {
		report.Error = err.Error()
		return report
	}
	if history.Version == 1 {
		report.Legacy = true
		report.Routes = summarizeRouteEntries(history.Routes)
		return report
	}
	profile := history.Profiles[report.Scope.ID]
	report.Routes = summarizeRouteEntries(profile.Routes)
	return report
}

func applyRouteHistory(routes []routeCandidate, report doctorHistoryReport) {
	if !report.Enabled || report.Error != "" {
		return
	}
	for index := range routes {
		kind := routeCandidateHistoryKind(routes[index])
		summary, ok := report.Routes[kind]
		if !ok || summary.Attempts == 0 {
			continue
		}
		routes[index].History = &summary
		routes[index].Reasons = append(routes[index].Reasons, fmt.Sprintf(
			"history: %d/%d successful",
			summary.Successes,
			summary.Attempts,
		))
		if summary.EWMABytesPerSecond > 0 {
			routes[index].Reasons = append(routes[index].Reasons, fmt.Sprintf(
				"history throughput %.1f MB/s",
				summary.EWMABytesPerSecond/(1024*1024),
			))
		}
		if len(summary.Paths) > 0 {
			weights := make([]string, 0, len(summary.Paths))
			for _, path := range summary.Paths {
				weights = append(weights, fmt.Sprintf("p%d=%.2f", path.Connection, path.Weight))
			}
			routes[index].Reasons = append(routes[index].Reasons, "historical path weights "+strings.Join(weights, " "))
		}
		if !routes[index].Available {
			continue
		}
		adjustment := int(math.Round((summary.SuccessRate - 0.5) * 20))
		adjustment -= min(summary.ConsecutiveFailures*3, 12)
		routes[index].Score = max(1, min(100, routes[index].Score+adjustment))
		if summary.ConsecutiveFailures > 0 {
			routes[index].Warnings = append(routes[index].Warnings, fmt.Sprintf(
				"%d consecutive historical failure(s)",
				summary.ConsecutiveFailures,
			))
		}
	}
	markPrimaryRoutes(routes)
}

func shouldDeferDirectFromHistory(report doctorHistoryReport, now time.Time) bool {
	if !report.Enabled || report.Error != "" {
		return false
	}
	summary, ok := report.Routes[historyRouteDirect]
	if !ok || summary.ConsecutiveFailures < directHistoryFailureThreshold || summary.LastFailure <= 0 {
		return false
	}
	age := now.Sub(time.UnixMilli(summary.LastFailure))
	return age >= 0 && age < directHistoryCooldown
}

func routeCandidateHistoryKind(candidate routeCandidate) string {
	switch candidate.Kind {
	case routeSignalDirect, routeDirectRelayFallback:
		return historyRouteDirect
	case routeRelayOnly:
		return historyRouteRelay
	default:
		return historyRouteWebRTC
	}
}

func routePathWeights(g *globalOptions, kind string, connectionCount int) []float64 {
	if connectionCount <= 0 {
		return nil
	}
	weights := make([]float64, connectionCount)
	for index := range weights {
		weights[index] = 1
	}
	if connectionCount <= 1 || !validHistoryRoute(kind) {
		return weights
	}
	report := inspectRouteHistory(g)
	if !report.Enabled || report.Error != "" {
		return weights
	}
	summary, ok := report.Routes[kind]
	if !ok {
		return weights
	}
	var totalRate float64
	var rateCount int
	for _, path := range summary.Paths {
		if path.Connection <= 0 || path.Connection >= len(weights) || path.EWMABytesPerSecond <= 0 {
			continue
		}
		totalRate += path.EWMABytesPerSecond
		rateCount++
	}
	if rateCount == 0 {
		return weights
	}
	averageRate := totalRate / float64(rateCount)
	for _, path := range summary.Paths {
		if path.Connection <= 0 || path.Connection >= len(weights) || path.EWMABytesPerSecond <= 0 {
			continue
		}
		weights[path.Connection] = clampRoutePathWeight(path.EWMABytesPerSecond / averageRate)
	}
	return weights
}

func clampRoutePathWeight(weight float64) float64 {
	if weight < 0.5 {
		return 0.5
	}
	if weight > 2 {
		return 2
	}
	return weight
}

func summarizeRouteEntries(routes map[string]routeHistoryEntry) map[string]routeHistorySummary {
	out := make(map[string]routeHistorySummary, len(routes))
	for kind, entry := range routes {
		summary := routeHistorySummary{
			Attempts:            entry.Attempts,
			Successes:           entry.Successes,
			Failures:            entry.Failures,
			ConsecutiveFailures: entry.ConsecutiveFailures,
			EWMABytesPerSecond:  entry.EWMABytesPerSecond,
			LastSuccess:         entry.LastSuccess,
			LastFailure:         entry.LastFailure,
		}
		if entry.Attempts > 0 {
			summary.SuccessRate = float64(entry.Successes) / float64(entry.Attempts)
			summary.AverageDurationMS = entry.DurationMillis / int64(entry.Attempts)
		}
		summary.Paths = summarizeRoutePaths(entry.Paths)
		out[kind] = summary
	}
	return out
}

func summarizeRoutePaths(paths map[string]routePathHistoryEntry) []routePathHistorySummary {
	out := make([]routePathHistorySummary, 0, len(paths))
	for rawConnection, entry := range paths {
		connection, err := strconv.Atoi(rawConnection)
		if err != nil || connection <= 0 {
			continue
		}
		out = append(out, routePathHistorySummary{
			Connection:         connection,
			Samples:            entry.Samples,
			SentBytes:          entry.SentBytes,
			EWMABytesPerSecond: entry.EWMABytesPerSecond,
			Weight:             1,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Connection < out[j].Connection
	})
	var total float64
	var count int
	for _, path := range out {
		if path.EWMABytesPerSecond > 0 {
			total += path.EWMABytesPerSecond
			count++
		}
	}
	if count == 0 {
		return out
	}
	average := total / float64(count)
	for index := range out {
		if out[index].EWMABytesPerSecond <= 0 {
			continue
		}
		out[index].Weight = clampRoutePathWeight(out[index].EWMABytesPerSecond / average)
	}
	return out
}

func newRouteHistory() routeHistoryFile {
	return routeHistoryFile{
		Version:  routeHistoryVersion,
		Profiles: map[string]routeHistoryProfile{},
	}
}

func migrateRouteHistory(history routeHistoryFile, scopeID string) routeHistoryFile {
	if history.Version == routeHistoryVersion {
		if history.Profiles == nil {
			history.Profiles = map[string]routeHistoryProfile{}
		}
		return history
	}
	migrated := newRouteHistory()
	routes := make(map[string]routeHistoryEntry, len(history.Routes))
	for kind, entry := range history.Routes {
		routes[kind] = entry
	}
	migrated.Profiles[scopeID] = routeHistoryProfile{
		LastSeen: time.Now().UnixMilli(),
		Routes:   routes,
	}
	return migrated
}

func pruneRouteHistoryProfiles(history *routeHistoryFile, keep string) {
	if history == nil || len(history.Profiles) <= maxRouteHistoryProfiles {
		return
	}
	type profileAge struct {
		id       string
		lastSeen int64
	}
	profiles := make([]profileAge, 0, len(history.Profiles))
	for id, profile := range history.Profiles {
		if id == keep {
			continue
		}
		profiles = append(profiles, profileAge{id: id, lastSeen: profile.LastSeen})
	}
	sort.Slice(profiles, func(i, j int) bool {
		if profiles[i].lastSeen == profiles[j].lastSeen {
			return profiles[i].id < profiles[j].id
		}
		return profiles[i].lastSeen < profiles[j].lastSeen
	})
	for len(history.Profiles) > maxRouteHistoryProfiles && len(profiles) > 0 {
		delete(history.Profiles, profiles[0].id)
		profiles = profiles[1:]
	}
}

func detectRouteNetworkScope(g *globalOptions) routeNetworkScope {
	if policy := selectedNetworkPolicy(g); policy != nil {
		ip := policy.IPv4()
		if ip == nil {
			ip = policy.IPv6()
		}
		return makeRouteNetworkScope(policy.InterfaceName(), ip, "selected-interface")
	}
	var loopback net.IP
	for _, endpoint := range routeScopeEndpoints(g) {
		ip := localIPForEndpoint(endpoint)
		if ip == nil {
			continue
		}
		if ip.IsLoopback() {
			loopback = ip
			continue
		}
		return routeNetworkScopeForIP(ip, "route")
	}
	if scope, ok := firstActiveNetworkScope(); ok {
		return scope
	}
	if loopback != nil {
		return routeNetworkScopeForIP(loopback, "loopback")
	}
	return routeNetworkScope{ID: defaultRouteHistoryScope, Source: "offline"}
}

func routeScopeEndpoints(g *globalOptions) []string {
	if g == nil {
		return nil
	}
	var endpoints []string
	if g.Relay != "" {
		if _, _, err := net.SplitHostPort(g.Relay); err == nil {
			endpoints = append(endpoints, g.Relay)
		}
	}
	if g.Signal != "" {
		raw := g.Signal
		if !strings.Contains(raw, "://") {
			raw = "http://" + raw
		}
		if parsed, err := url.Parse(raw); err == nil && parsed.Hostname() != "" {
			port := parsed.Port()
			if port == "" {
				if parsed.Scheme == "https" || parsed.Scheme == "wss" {
					port = "443"
				} else {
					port = "80"
				}
			}
			endpoints = append(endpoints, net.JoinHostPort(parsed.Hostname(), port))
		}
	}
	return uniqueStrings(endpoints)
}

func localIPForEndpoint(endpoint string) net.IP {
	conn, err := net.DialTimeout("udp", endpoint, 200*time.Millisecond)
	if err != nil {
		return nil
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return nil
	}
	return addr.IP
}

func firstActiveNetworkScope() (routeNetworkScope, bool) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return routeNetworkScope{}, false
	}
	sort.Slice(ifaces, func(i, j int) bool {
		if ifaces[i].Index == ifaces[j].Index {
			return ifaces[i].Name < ifaces[j].Name
		}
		return ifaces[i].Index < ifaces[j].Index
	})
	var ipv6 net.IP
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ip := ipFromAddr(addr)
			if !usableRouteScopeIP(ip) {
				continue
			}
			if ip.To4() != nil {
				return makeRouteNetworkScope(iface.Name, ip, "interface"), true
			}
			if ipv6 == nil {
				ipv6 = append(net.IP(nil), ip...)
			}
		}
	}
	if ipv6 != nil {
		return routeNetworkScopeForIP(ipv6, "interface"), true
	}
	return routeNetworkScope{}, false
}

func routeNetworkScopeForIP(ip net.IP, source string) routeNetworkScope {
	ifaces, _ := net.Interfaces()
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			candidate := ipFromAddr(addr)
			if candidate != nil && candidate.Equal(ip) {
				return makeRouteNetworkScope(iface.Name, ip, source)
			}
		}
	}
	return makeRouteNetworkScope("", ip, source)
}

func makeRouteNetworkScope(interfaceName string, ip net.IP, source string) routeNetworkScope {
	family := "ipv6"
	normalized := ip.Mask(net.CIDRMask(64, 128))
	if ip4 := ip.To4(); ip4 != nil {
		family = "ipv4"
		normalized = ip4
	}
	canonical := strings.Join([]string{
		"kigo-network-scope-v1",
		interfaceName,
		family,
		normalized.String(),
	}, "|")
	digest := sha256.Sum256([]byte(canonical))
	return routeNetworkScope{
		ID:        fmt.Sprintf("%x", digest[:8]),
		Interface: interfaceName,
		Family:    family,
		Source:    source,
	}
}

func usableRouteScopeIP(ip net.IP) bool {
	return ip != nil &&
		!ip.IsLoopback() &&
		!ip.IsUnspecified() &&
		!ip.IsLinkLocalUnicast()
}

func loadRouteHistory(path string) (routeHistoryFile, error) {
	file, err := os.Open(path)
	if err != nil {
		return routeHistoryFile{}, err
	}
	defer file.Close()
	var history routeHistoryFile
	decoder := json.NewDecoder(io.LimitReader(file, maxRouteHistoryBytes))
	if err := decoder.Decode(&history); err != nil {
		return routeHistoryFile{}, fmt.Errorf("read route history: %w", err)
	}
	if history.Version != 1 && history.Version != routeHistoryVersion {
		return routeHistoryFile{}, fmt.Errorf("unsupported route history version %d", history.Version)
	}
	if history.Version == 1 && history.Routes == nil {
		history.Routes = map[string]routeHistoryEntry{}
	}
	if history.Version == routeHistoryVersion && history.Profiles == nil {
		history.Profiles = map[string]routeHistoryProfile{}
	}
	return history, nil
}

func validHistoryRoute(kind string) bool {
	return kind == historyRouteDirect || kind == historyRouteRelay || kind == historyRouteWebRTC
}
