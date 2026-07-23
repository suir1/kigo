package app

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/suir1/kigo/internal/netpolicy"
)

func TestSelectedInterfaceControlsDirectListenAdvertiseAndDoctor(t *testing.T) {
	interfaceName := appInterfaceForIP(t, net.ParseIP("127.0.0.1"))
	policy, err := netpolicy.Resolve(interfaceName)
	if err != nil {
		t.Fatal(err)
	}
	g := &globalOptions{
		Interface:     interfaceName,
		DirectListen:  ":0",
		networkPolicy: policy,
	}
	listen := directListenAddress(g)
	host, _, err := net.SplitHostPort(listen)
	if err != nil || !policy.ContainsIP(net.ParseIP(host)) {
		t.Fatalf("direct listen = %q, policy addresses = %v, err = %v", listen, policy.IPs(), err)
	}
	hosts := policyAdvertiseHosts(g)
	if len(hosts) == 0 || !policy.ContainsIP(net.ParseIP(hosts[0])) {
		t.Fatalf("advertise hosts = %v, policy addresses = %v", hosts, policy.IPs())
	}
	report := inspectNetworkPolicy(g)
	if report.Policy != "interface" || report.Interface != interfaceName || len(report.Addresses) == 0 {
		t.Fatalf("network report = %#v", report)
	}
	scope := detectRouteNetworkScope(g)
	if scope.Interface != interfaceName || scope.Source != "selected-interface" {
		t.Fatalf("route scope = %#v", scope)
	}
}

func TestSelectedInterfaceRejectsConflictingDirectListen(t *testing.T) {
	interfaceName := appInterfaceForIP(t, net.ParseIP("127.0.0.1"))
	policy, err := netpolicy.Resolve(interfaceName)
	if err != nil {
		t.Fatal(err)
	}
	g := &globalOptions{
		DirectListen:  "192.0.2.20:0",
		networkPolicy: policy,
	}
	err = validateDirectListenPolicy(g)
	if err == nil || !strings.Contains(err.Error(), interfaceName) {
		t.Fatalf("conflicting listen error = %v", err)
	}
}

func TestOutboundTargetPrefersRelayAndRecognizesLoopback(t *testing.T) {
	target, err := outboundTarget(&globalOptions{Relay: "127.0.0.1:9000", Signal: "https://signal.example"})
	if err != nil {
		t.Fatal(err)
	}
	if target.Kind != "relay_tcp" || !target.Local || target.Address != "127.0.0.1:9000" {
		t.Fatalf("relay target = %#v", target)
	}
	target, err = outboundTarget(&globalOptions{Signal: "https://signal.example/base"})
	if err != nil {
		t.Fatal(err)
	}
	if target.Kind != "signaling_http" || target.Local || target.URL != "https://signal.example/base/api/health" {
		t.Fatalf("signaling target = %#v", target)
	}
}

func TestOutboundHTTPProbeUsesSelectedInterface(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	interfaceName := appInterfaceForIP(t, net.ParseIP("127.0.0.1"))
	policy, err := netpolicy.Resolve(interfaceName)
	if err != nil {
		t.Fatal(err)
	}
	target := outboundProbeTarget{Kind: "signaling_http", URL: server.URL + "/api/health"}
	probe := probeOutboundPath(context.Background(), target, netpolicy.PathPhysical, policy, nil)
	if !probe.OK || probe.Interface != interfaceName || probe.LatencyMillis <= 0 || probe.Error != "" {
		t.Fatalf("HTTP probe = %#v", probe)
	}
}

func appInterfaceForIP(t *testing.T, target net.IP) string {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			if ip := ipFromAddr(address); ip != nil && ip.Equal(target) {
				return iface.Name
			}
		}
	}
	t.Fatalf("no interface owns %s", target)
	return ""
}
