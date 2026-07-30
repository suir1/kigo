package netpolicy

import (
	"net"
	"slices"
	"sort"
	"strings"
)

const RTTWinMarginMillis int64 = 5

const (
	PathDefault  = "default"
	PathPhysical = "physical"
	PathForced   = "forced"
)

const (
	ReasonUserForcedInterface      = "user_forced_interface"
	ReasonLocalTarget              = "local_target"
	ReasonProxyDefault             = "proxy_default"
	ReasonAvoidVPN                 = "avoid_vpn"
	ReasonAvoidVPNNoPhysical       = "avoid_vpn_no_physical_interface"
	ReasonAutoDisabled             = "auto_interface_disabled"
	ReasonNoVPN                    = "no_vpn_detected"
	ReasonNoPhysical               = "no_physical_interface"
	ReasonProbeRequired            = "probe_required"
	ReasonDefaultFailedPhysicalOK  = "default_failed_physical_ok"
	ReasonPhysicalLowerRTT         = "physical_lower_rtt"
	ReasonDefaultLowerOrSimilarRTT = "default_lower_or_similar_rtt"
	ReasonPhysicalFailedDefaultOK  = "physical_failed_default_ok"
	ReasonBothPathsFailed          = "both_paths_failed"
)

// Interface describes one active interface with usable unicast addresses.
type Interface struct {
	Name      string   `json:"name"`
	Addresses []string `json:"addresses,omitempty"`
	VPN       bool     `json:"vpn,omitempty"`
	Loopback  bool     `json:"loopback,omitempty"`
}

// Inventory is a portable view used by the outbound selector.
type Inventory struct {
	Interfaces []Interface `json:"interfaces,omitempty"`
	VPNPresent bool        `json:"vpn_present"`
}

// Probe records reachability through one candidate outbound path.
type Probe struct {
	Path          string `json:"path"`
	Interface     string `json:"interface,omitempty"`
	OK            bool   `json:"ok"`
	LatencyMillis int64  `json:"latency_ms,omitempty"`
	Error         string `json:"error,omitempty"`
}

type OutboundOptions struct {
	ExplicitInterface string
	Proxy             bool
	LocalTarget       bool
	AvoidVPN          bool
	AutoEnabled       bool
	Inventory         Inventory
	Probed            bool
	DefaultProbe      Probe
	PhysicalProbe     Probe
}

type OutboundSelection struct {
	Evaluated         bool    `json:"-"`
	Path              string  `json:"path"`
	Interface         string  `json:"interface,omitempty"`
	Reason            string  `json:"reason"`
	VPNDetected       bool    `json:"vpn_detected"`
	PreferredPhysical string  `json:"preferred_physical,omitempty"`
	Probes            []Probe `json:"probes,omitempty"`
}

// CollectInventory returns active interfaces and records VPN interface
// presence even when a tunnel only has a link-local address.
func CollectInventory() Inventory {
	var inventory Inventory
	interfaces, err := net.Interfaces()
	if err != nil {
		return inventory
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		vpn := IsVPNInterfaceName(iface.Name)
		if vpn {
			inventory.VPNPresent = true
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		entry := Interface{
			Name:     iface.Name,
			VPN:      vpn,
			Loopback: iface.Flags&net.FlagLoopback != 0,
		}
		for _, address := range addresses {
			ip := addressIP(address)
			if !usableIP(ip) || slices.Contains(entry.Addresses, ip.String()) {
				continue
			}
			entry.Addresses = append(entry.Addresses, ip.String())
		}
		if len(entry.Addresses) > 0 {
			inventory.Interfaces = append(inventory.Interfaces, entry)
		}
	}
	sort.Slice(inventory.Interfaces, func(i, j int) bool {
		return inventory.Interfaces[i].Name < inventory.Interfaces[j].Name
	})
	return inventory
}

func (inventory Inventory) VPNDetected() bool {
	if inventory.VPNPresent {
		return true
	}
	for _, iface := range inventory.Interfaces {
		if iface.VPN && !iface.Loopback {
			return true
		}
	}
	return false
}

// PreferredPhysicalInterface scores common physical adapter names while
// excluding tunnels, loopback, bridges, and VM/container adapters.
func (inventory Inventory) PreferredPhysicalInterface() (string, bool) {
	scores := make(map[string]int)
	for _, iface := range inventory.Interfaces {
		if iface.Loopback || iface.VPN || isLikelyVirtualInterfaceName(iface.Name) {
			continue
		}
		for _, address := range iface.Addresses {
			score := physicalInterfaceNameScore(iface.Name)
			if ip := net.ParseIP(address); ip != nil && ip.To4() != nil {
				score += 20
			}
			scores[iface.Name] += score
		}
	}
	bestName := ""
	bestScore := -1
	for name, score := range scores {
		if score > bestScore || (score == bestScore && (bestName == "" || name < bestName)) {
			bestName = name
			bestScore = score
		}
	}
	return bestName, bestName != ""
}

func IsVPNInterfaceName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, prefix := range []string{"utun", "tun", "tap", "wg", "ppp", "ipsec", "tailscale", "zt"} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// TargetIsLocal recognizes targets that cannot benefit from an outbound
// interface override. Private LAN addresses remain probeable because a VPN may
// still intercept them.
func TargetIsLocal(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if zone := strings.LastIndexByte(host, '%'); zone >= 0 {
		host = host[:zone]
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// SelectOutbound implements the route decision independently from probing so
// its behavior can be tested without live network dependencies.
func SelectOutbound(options OutboundOptions) OutboundSelection {
	physical, hasPhysical := options.Inventory.PreferredPhysicalInterface()
	selection := OutboundSelection{
		Evaluated:         true,
		Path:              PathDefault,
		Reason:            ReasonNoVPN,
		VPNDetected:       options.Inventory.VPNDetected(),
		PreferredPhysical: physical,
	}
	switch {
	case strings.TrimSpace(options.ExplicitInterface) != "":
		selection.Path = PathForced
		selection.Interface = strings.TrimSpace(options.ExplicitInterface)
		selection.Reason = ReasonUserForcedInterface
		return selection
	case options.LocalTarget:
		selection.Reason = ReasonLocalTarget
		return selection
	case options.Proxy:
		selection.Reason = ReasonProxyDefault
		return selection
	case options.AvoidVPN:
		if hasPhysical {
			selection.Path = PathPhysical
			selection.Interface = physical
			selection.Reason = ReasonAvoidVPN
		} else {
			selection.Reason = ReasonAvoidVPNNoPhysical
		}
		return selection
	case !options.AutoEnabled:
		selection.Reason = ReasonAutoDisabled
		return selection
	case !selection.VPNDetected:
		selection.Reason = ReasonNoVPN
		return selection
	case !hasPhysical:
		selection.Reason = ReasonNoPhysical
		return selection
	case !options.Probed:
		selection.Reason = ReasonProbeRequired
		return selection
	}

	selection.Probes = []Probe{options.DefaultProbe, options.PhysicalProbe}
	physicalProbe := options.PhysicalProbe
	defaultProbe := options.DefaultProbe
	switch {
	case physicalProbe.OK && !defaultProbe.OK:
		selection.Path = PathPhysical
		selection.Interface = physical
		selection.Reason = ReasonDefaultFailedPhysicalOK
	case physicalProbe.OK && defaultProbe.OK && MateriallyFaster(physicalProbe.LatencyMillis, defaultProbe.LatencyMillis):
		selection.Path = PathPhysical
		selection.Interface = physical
		selection.Reason = ReasonPhysicalLowerRTT
	case defaultProbe.OK && physicalProbe.OK:
		selection.Reason = ReasonDefaultLowerOrSimilarRTT
	case defaultProbe.OK:
		selection.Reason = ReasonPhysicalFailedDefaultOK
	default:
		selection.Reason = ReasonBothPathsFailed
	}
	return selection
}

func MateriallyFaster(candidateMillis, currentMillis int64) bool {
	return candidateMillis >= 0 && currentMillis >= 0 && candidateMillis+RTTWinMarginMillis < currentMillis
}

func isLikelyVirtualInterfaceName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || IsVPNInterfaceName(name) {
		return true
	}
	for _, prefix := range []string{
		"lo", "awdl", "llw", "bridge", "vmenet", "vmnet", "vethernet",
		"docker", "br-", "gif", "stf",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func physicalInterfaceNameScore(name string) int {
	name = strings.ToLower(name)
	switch {
	case name == "en0":
		return 120
	case name == "en1":
		return 110
	case strings.HasPrefix(name, "en"):
		return 90
	case strings.HasPrefix(name, "eth"), strings.HasPrefix(name, "wl"):
		return 80
	default:
		return 10
	}
}
