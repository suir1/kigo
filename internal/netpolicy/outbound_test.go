package netpolicy

import "testing"

func TestInterfaceClassificationAndPreferredPhysical(t *testing.T) {
	for _, name := range []string{"utun7", "tun0", "tap0", "wg0", "ppp0", "ipsec0", "tailscale0", "ztabc"} {
		if !IsVPNInterfaceName(name) {
			t.Fatalf("VPN interface %q was not recognized", name)
		}
	}
	for _, name := range []string{"en0", "eth0", "wlan0", "bridge0"} {
		if IsVPNInterfaceName(name) {
			t.Fatalf("physical/virtual interface %q was classified as VPN", name)
		}
	}
	inventory := Inventory{
		VPNPresent: true,
		Interfaces: []Interface{
			{Name: "bridge0", Addresses: []string{"10.0.0.1"}},
			{Name: "eth0", Addresses: []string{"192.168.2.10"}},
			{Name: "en0", Addresses: []string{"192.168.1.10", "2001:db8::10"}},
			{Name: "utun7", Addresses: []string{"198.18.0.1"}, VPN: true},
		},
	}
	physical, ok := inventory.PreferredPhysicalInterface()
	if !ok || physical != "en0" || !inventory.VPNDetected() {
		t.Fatalf("inventory physical=%q ok=%v vpn=%v", physical, ok, inventory.VPNDetected())
	}
}

func TestSelectOutboundPreconditions(t *testing.T) {
	inventory := outboundTestInventory()
	tests := []struct {
		name      string
		options   OutboundOptions
		path      string
		iface     string
		reason    string
		probesLen int
	}{
		{
			name: "explicit interface wins",
			options: OutboundOptions{ExplicitInterface: "en9", Proxy: true, LocalTarget: true,
				AvoidVPN: true, AutoEnabled: true, Inventory: inventory},
			path: PathForced, iface: "en9", reason: ReasonUserForcedInterface,
		},
		{
			name:    "local target",
			options: OutboundOptions{LocalTarget: true, AvoidVPN: true, AutoEnabled: true, Inventory: inventory},
			path:    PathDefault, reason: ReasonLocalTarget,
		},
		{
			name:    "proxy",
			options: OutboundOptions{Proxy: true, AvoidVPN: true, AutoEnabled: true, Inventory: inventory},
			path:    PathDefault, reason: ReasonProxyDefault,
		},
		{
			name:    "avoid VPN",
			options: OutboundOptions{AvoidVPN: true, AutoEnabled: true, Inventory: inventory},
			path:    PathPhysical, iface: "en0", reason: ReasonAvoidVPN,
		},
		{
			name:    "automatic disabled",
			options: OutboundOptions{AutoEnabled: false, Inventory: inventory},
			path:    PathDefault, reason: ReasonAutoDisabled,
		},
		{
			name:    "probe requested",
			options: OutboundOptions{AutoEnabled: true, Inventory: inventory},
			path:    PathDefault, reason: ReasonProbeRequired,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selection := SelectOutbound(test.options)
			if selection.Path != test.path || selection.Interface != test.iface || selection.Reason != test.reason ||
				len(selection.Probes) != test.probesLen {
				t.Fatalf("selection = %#v", selection)
			}
		})
	}
}

func TestSelectOutboundProbeResults(t *testing.T) {
	inventory := outboundTestInventory()
	tests := []struct {
		name     string
		def      Probe
		physical Probe
		path     string
		reason   string
	}{
		{
			name:     "default fails",
			def:      Probe{Path: PathDefault, Error: "blocked"},
			physical: Probe{Path: PathPhysical, Interface: "en0", OK: true, LatencyMillis: 40},
			path:     PathPhysical, reason: ReasonDefaultFailedPhysicalOK,
		},
		{
			name:     "physical materially faster",
			def:      Probe{Path: PathDefault, OK: true, LatencyMillis: 90},
			physical: Probe{Path: PathPhysical, Interface: "en0", OK: true, LatencyMillis: 40},
			path:     PathPhysical, reason: ReasonPhysicalLowerRTT,
		},
		{
			name:     "difference at margin retains default",
			def:      Probe{Path: PathDefault, OK: true, LatencyMillis: 45},
			physical: Probe{Path: PathPhysical, Interface: "en0", OK: true, LatencyMillis: 40},
			path:     PathDefault, reason: ReasonDefaultLowerOrSimilarRTT,
		},
		{
			name:     "physical fails",
			def:      Probe{Path: PathDefault, OK: true, LatencyMillis: 20},
			physical: Probe{Path: PathPhysical, Interface: "en0", Error: "blocked"},
			path:     PathDefault, reason: ReasonPhysicalFailedDefaultOK,
		},
		{
			name:     "both fail",
			def:      Probe{Path: PathDefault, Error: "blocked"},
			physical: Probe{Path: PathPhysical, Interface: "en0", Error: "blocked"},
			path:     PathDefault, reason: ReasonBothPathsFailed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selection := SelectOutbound(OutboundOptions{
				AutoEnabled: true, Inventory: inventory, Probed: true,
				DefaultProbe: test.def, PhysicalProbe: test.physical,
			})
			if selection.Path != test.path || selection.Reason != test.reason || len(selection.Probes) != 2 {
				t.Fatalf("selection = %#v", selection)
			}
			if test.path == PathPhysical && selection.Interface != "en0" {
				t.Fatalf("physical selection = %#v", selection)
			}
		})
	}
}

func TestTargetIsLocal(t *testing.T) {
	for _, host := range []string{"localhost", "127.0.0.1", "::1", "[::1]"} {
		if !TargetIsLocal(host) {
			t.Fatalf("local target %q was not recognized", host)
		}
	}
	for _, host := range []string{"192.168.1.5", "relay.example", "203.0.113.10"} {
		if TargetIsLocal(host) {
			t.Fatalf("remote target %q was classified as local", host)
		}
	}
}

func outboundTestInventory() Inventory {
	return Inventory{
		VPNPresent: true,
		Interfaces: []Interface{
			{Name: "en0", Addresses: []string{"192.168.1.10"}},
			{Name: "utun7", Addresses: []string{"198.18.0.1"}, VPN: true},
		},
	}
}
