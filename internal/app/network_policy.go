package app

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/suir1/kigo/internal/netpolicy"
	"github.com/suir1/kigo/internal/netprobe"
	"github.com/suir1/kigo/internal/netproxy"
	"github.com/suir1/kigo/internal/netreuse"
	"github.com/suir1/kigo/internal/relay"
)

const autoOutboundProbeTimeout = 900 * time.Millisecond

type outboundProbeTarget struct {
	Kind    string
	Address string
	URL     string
	Host    string
	Local   bool
}

func (target outboundProbeTarget) Label() string {
	if target.Address != "" {
		return target.Address
	}
	return target.URL
}

func configureNetworkPolicy(ctx context.Context, g *globalOptions, autoAllowed bool) error {
	if g == nil {
		return nil
	}
	target := outboundProbeTarget{Kind: "none", Local: true}
	var err error
	if autoAllowed {
		target, err = outboundTarget(g)
		if err != nil {
			return err
		}
	}
	inventory := netpolicy.CollectInventory()
	options := netpolicy.OutboundOptions{
		ExplicitInterface: g.Interface,
		Proxy:             strings.TrimSpace(g.Proxy) != "",
		LocalTarget:       target.Local,
		AvoidVPN:          g.AvoidVPN,
		AutoEnabled:       autoAllowed && !g.NoAutoInterface,
		Inventory:         inventory,
	}
	selection := netpolicy.SelectOutbound(options)
	if selection.Reason == netpolicy.ReasonProbeRequired {
		physicalPolicy, resolveErr := netpolicy.Resolve(selection.PreferredPhysical)
		if resolveErr != nil {
			return resolveErr
		}
		defaultProbe, physicalProbe := probeOutboundPaths(ctx, target, physicalPolicy, clientTLSConfig(g))
		options.Probed = true
		options.DefaultProbe = defaultProbe
		options.PhysicalProbe = physicalProbe
		selection = netpolicy.SelectOutbound(options)
	}
	policy, err := netpolicy.Resolve(selection.Interface)
	if err != nil {
		return err
	}
	g.networkPolicy = policy
	g.outboundSelection = selection
	g.outboundTarget = target
	if selection.Interface != "" {
		g.Interface = selection.Interface
	}
	return nil
}

func outboundTarget(g *globalOptions) (outboundProbeTarget, error) {
	if g == nil || g.Local {
		return outboundProbeTarget{Kind: "lan", Local: true}, nil
	}
	if strings.TrimSpace(g.Relay) != "" {
		host, _, err := net.SplitHostPort(g.Relay)
		if err != nil {
			return outboundProbeTarget{}, fmt.Errorf("relay target: %w", err)
		}
		return outboundProbeTarget{
			Kind:    "relay_tcp",
			Address: g.Relay,
			Host:    host,
			Local:   netpolicy.TargetIsLocal(host),
		}, nil
	}
	healthURL, err := apiURL(g.Signal, "/api/health")
	if err != nil {
		return outboundProbeTarget{}, err
	}
	parsed, err := url.Parse(healthURL)
	if err != nil {
		return outboundProbeTarget{}, fmt.Errorf("signaling health URL: %w", err)
	}
	return outboundProbeTarget{
		Kind:  "signaling_http",
		URL:   healthURL,
		Host:  parsed.Hostname(),
		Local: netpolicy.TargetIsLocal(parsed.Hostname()),
	}, nil
}

func probeOutboundPaths(
	ctx context.Context,
	target outboundProbeTarget,
	physicalPolicy *netpolicy.Policy,
	tlsConfig *tls.Config,
) (netpolicy.Probe, netpolicy.Probe) {
	results := make(chan netpolicy.Probe, 2)
	go func() {
		results <- probeOutboundPath(ctx, target, netpolicy.PathDefault, nil, tlsConfig)
	}()
	go func() {
		results <- probeOutboundPath(ctx, target, netpolicy.PathPhysical, physicalPolicy, tlsConfig)
	}()
	var defaultProbe, physicalProbe netpolicy.Probe
	for range 2 {
		probe := <-results
		if probe.Path == netpolicy.PathPhysical {
			physicalProbe = probe
		} else {
			defaultProbe = probe
		}
	}
	return defaultProbe, physicalProbe
}

func probeOutboundPath(
	parent context.Context,
	target outboundProbeTarget,
	path string,
	policy *netpolicy.Policy,
	tlsConfig *tls.Config,
) netpolicy.Probe {
	probe := netpolicy.Probe{Path: path}
	if policy != nil {
		probe.Interface = policy.InterfaceName()
	}
	ctx, cancel := context.WithTimeout(parent, autoOutboundProbeTimeout)
	defer cancel()
	started := time.Now()
	var err error
	switch target.Kind {
	case "relay_tcp":
		var conn net.Conn
		if policy == nil {
			var dialer net.Dialer
			conn, err = dialer.DialContext(ctx, "tcp", target.Address)
		} else {
			conn, err = policy.DialContext(ctx, "tcp", target.Address)
		}
		if err == nil {
			_ = conn.Close()
		}
	case "signaling_http":
		err = probeOutboundHTTP(ctx, target.URL, policy, tlsConfig)
	default:
		err = fmt.Errorf("unsupported outbound probe target %q", target.Kind)
	}
	probe.LatencyMillis = elapsedMillis(started)
	if err != nil {
		probe.Error = err.Error()
		return probe
	}
	probe.OK = true
	return probe
}

func probeOutboundHTTP(ctx context.Context, targetURL string, policy *netpolicy.Policy, tlsConfig *tls.Config) error {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if ok {
		transport = transport.Clone()
	} else {
		transport = &http.Transport{}
	}
	if policy != nil {
		transport.DialContext = policy.DialContext
	}
	if tlsConfig != nil {
		transport.TLSClientConfig = tlsConfig.Clone()
	}
	defer transport.CloseIdleConnections()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return err
	}
	response, err := (&http.Client{Transport: transport}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("%s returned %s", targetURL, response.Status)
	}
	return nil
}

func selectedNetworkPolicy(g *globalOptions) *netpolicy.Policy {
	if g == nil {
		return nil
	}
	return g.networkPolicy
}

func outboundDialContext(g *globalOptions) relay.DialContextFunc {
	if policy := selectedNetworkPolicy(g); policy != nil {
		return policy.DialContext
	}
	var dialer net.Dialer
	return dialer.DialContext
}

func outboundHTTPClient(g *globalOptions) *http.Client {
	policy := selectedNetworkPolicy(g)
	tlsConfig := clientTLSConfig(g)
	if policy == nil && tlsConfig == nil {
		return http.DefaultClient
	}
	transport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		transport = &http.Transport{}
	} else {
		transport = transport.Clone()
	}
	if policy != nil {
		transport.DialContext = policy.DialContext
	}
	if tlsConfig != nil {
		transport.TLSClientConfig = tlsConfig
	}
	return &http.Client{Transport: transport}
}

func outboundWebSocketDialer(g *globalOptions) *websocket.Dialer {
	dialer := *websocket.DefaultDialer
	if policy := selectedNetworkPolicy(g); policy != nil {
		dialer.NetDialContext = policy.DialContext
	}
	if tlsConfig := clientTLSConfig(g); tlsConfig != nil {
		dialer.TLSClientConfig = tlsConfig
	}
	return &dialer
}

func webRTCDialContext(g *globalOptions) func(context.Context, string, string) (net.Conn, error) {
	if selectedNetworkPolicy(g) == nil {
		return nil
	}
	return outboundDialContext(g)
}

func webRTCInterfaceFilter(g *globalOptions) func(string) bool {
	policy := selectedNetworkPolicy(g)
	if policy == nil {
		return nil
	}
	return policy.InterfaceFilter
}

func webRTCIPFilter(g *globalOptions) func(net.IP) bool {
	policy := selectedNetworkPolicy(g)
	if policy == nil {
		return nil
	}
	return policy.IPFilter
}

func outboundSTUNOptions(g *globalOptions) netprobe.STUNOptions {
	policy := selectedNetworkPolicy(g)
	if policy == nil {
		return netprobe.STUNOptions{}
	}
	return netprobe.STUNOptions{
		IPv4:         policy.IPv4(),
		IPv6:         policy.IPv6(),
		InterfaceIPs: policy.IPs(),
	}
}

func outboundProxyConfig(g *globalOptions) (*netproxy.Config, error) {
	if g == nil {
		return nil, nil
	}
	config, err := netproxy.Parse(g.Proxy)
	if err != nil {
		return nil, err
	}
	if config != nil && selectedNetworkPolicy(g) != nil {
		config = config.WithDialContext(netproxy.DialContextFunc(outboundDialContext(g)))
	}
	return config, nil
}

func outboundDirectDialer(g *globalOptions) directDialContextFunc {
	policy := selectedNetworkPolicy(g)
	if policy == nil {
		return nil
	}
	return func(ctx context.Context, address string, localPort int) (net.Conn, error) {
		network, source, err := policy.TCPAddrFor(address, localPort)
		if err != nil {
			return nil, err
		}
		dialer := netreuse.TCPDialerForIP(localPort, source.IP)
		return dialer.DialContext(ctx, network, address)
	}
}

func directListenAddress(g *globalOptions) string {
	if g == nil || selectedNetworkPolicy(g) == nil {
		if g == nil {
			return ":0"
		}
		return g.DirectListen
	}
	host, port, err := net.SplitHostPort(g.DirectListen)
	if err != nil || (host != "" && host != "0.0.0.0" && host != "::") {
		return g.DirectListen
	}
	policy := selectedNetworkPolicy(g)
	ip := policy.IPv4()
	if ip == nil {
		ip = policy.IPv6()
	}
	return net.JoinHostPort(ip.String(), port)
}

func validateDirectListenPolicy(g *globalOptions) error {
	policy := selectedNetworkPolicy(g)
	if policy == nil || g == nil {
		return nil
	}
	host, _, err := net.SplitHostPort(g.DirectListen)
	if err != nil {
		return fmt.Errorf("direct listen address: %w", err)
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if host == "" || host == "0.0.0.0" || host == "::" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return errorsForInterfaceListen(policy.InterfaceName(), host)
	}
	if !policy.ContainsIP(ip) {
		return errorsForInterfaceListen(policy.InterfaceName(), host)
	}
	return nil
}

func errorsForInterfaceListen(interfaceName, host string) error {
	return fmt.Errorf("direct listen address %s does not belong to interface %s", host, interfaceName)
}

func policyAdvertiseHosts(g *globalOptions) []string {
	policy := selectedNetworkPolicy(g)
	if policy == nil {
		return advertiseHosts()
	}
	ips := policy.IPs()
	hosts := make([]string, 0, len(ips))
	for _, ip := range ips {
		hosts = append(hosts, ip.String())
	}
	return hosts
}

func inspectNetworkPolicy(g *globalOptions) doctorNetworkReport {
	policy := selectedNetworkPolicy(g)
	selection := netpolicy.OutboundSelection{
		Path:   netpolicy.PathDefault,
		Reason: "not_evaluated",
	}
	if g != nil && g.outboundSelection.Evaluated {
		selection = g.outboundSelection
	} else if policy != nil {
		selection.Path = netpolicy.PathForced
		selection.Interface = policy.InterfaceName()
		selection.Reason = netpolicy.ReasonUserForcedInterface
	}
	report := doctorNetworkReport{
		Policy:            "auto",
		Path:              selection.Path,
		Reason:            selection.Reason,
		Interface:         selection.Interface,
		VPNDetected:       selection.VPNDetected,
		PreferredPhysical: selection.PreferredPhysical,
		Probes:            append([]netpolicy.Probe(nil), selection.Probes...),
	}
	if g != nil {
		report.ProbeTargetKind = g.outboundTarget.Kind
		report.ProbeTarget = g.outboundTarget.Label()
	}
	if policy != nil {
		report.Policy = "interface"
		report.Interface = policy.InterfaceName()
		for _, ip := range policy.IPs() {
			report.Addresses = append(report.Addresses, ip.String())
		}
	}
	return report
}

func printDoctorNetwork(out io.Writer, report doctorNetworkReport) {
	if report.Policy != "interface" {
		fmt.Fprintf(out, "- network policy: automatic OS routing path=%s reason=%s\n", report.Path, report.Reason)
	} else {
		fmt.Fprintf(
			out,
			"- network policy: interface=%s path=%s reason=%s addresses=%s\n",
			report.Interface,
			report.Path,
			report.Reason,
			strings.Join(report.Addresses, ","),
		)
	}
	if report.VPNDetected {
		fmt.Fprintf(out, "  - VPN/TUN detected; preferred physical interface=%s\n", fallbackString(report.PreferredPhysical, "none"))
	}
	if report.ProbeTarget != "" {
		fmt.Fprintf(out, "  - probe target: %s %s\n", report.ProbeTargetKind, report.ProbeTarget)
	}
	for _, probe := range report.Probes {
		path := probe.Path
		if probe.Interface != "" {
			path += "/" + probe.Interface
		}
		if probe.OK {
			fmt.Fprintf(out, "  - outbound probe: %s ok latency=%dms\n", path, probe.LatencyMillis)
		} else {
			fmt.Fprintf(out, "  - outbound probe: %s failed (%s)\n", path, probe.Error)
		}
	}
}

func fallbackString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
