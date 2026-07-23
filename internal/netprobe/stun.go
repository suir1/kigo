package netprobe

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/pion/stun/v3"
)

const maxSTUNServers = 2

type NATClass string

const (
	NATUnknown   NATClass = "unknown"
	NATOpen      NATClass = "open"
	NATCone      NATClass = "cone"
	NATSymmetric NATClass = "symmetric"
)

func ValidNATClass(value string) bool {
	switch NATClass(value) {
	case NATUnknown, NATOpen, NATCone, NATSymmetric:
		return true
	default:
		return false
	}
}

type STUNObservation struct {
	Server        string `json:"server"`
	Mapped        string `json:"mapped,omitempty"`
	LatencyMillis int64  `json:"latency_ms,omitempty"`
	Error         string `json:"error,omitempty"`
}

type STUNReport struct {
	OK           bool              `json:"ok"`
	Class        NATClass          `json:"class"`
	Local        string            `json:"local,omitempty"`
	Observations []STUNObservation `json:"observations,omitempty"`
	Error        string            `json:"error,omitempty"`
}

type STUNOptions struct {
	IPv4         net.IP
	IPv6         net.IP
	InterfaceIPs []net.IP
}

type stunEndpoint struct {
	raw  string
	host string
	port int
}

type stunTarget struct {
	raw  string
	addr *net.UDPAddr
}

type bindingResult struct {
	mapped *net.UDPAddr
	other  *net.UDPAddr
}

// ProbeSTUN compares UDP mappings from up to two STUN destinations. The result
// is a routing heuristic; it does not guarantee equivalent TCP NAT behavior.
func ProbeSTUN(ctx context.Context, urls []string, timeout time.Duration) STUNReport {
	return ProbeSTUNWithOptions(ctx, urls, timeout, STUNOptions{})
}

func ProbeSTUNWithOptions(ctx context.Context, urls []string, timeout time.Duration, opts STUNOptions) STUNReport {
	report, conn := probeSTUNWithOptions(ctx, urls, timeout, opts)
	if conn != nil {
		_ = conn.Close()
	}
	return report
}

// ProbeSTUNForPunch performs the normal mapping probe but keeps the probed UDP
// socket open so the caller can use the same NAT mapping for a peer poke.
func ProbeSTUNForPunch(
	ctx context.Context,
	urls []string,
	timeout time.Duration,
	opts STUNOptions,
) (STUNReport, *UDPPuncher) {
	report, conn := probeSTUNWithOptions(ctx, urls, timeout, opts)
	if conn == nil {
		return report, nil
	}
	puncher := newUDPPuncher(conn, report, opts)
	if len(puncher.Candidates()) == 0 {
		_ = puncher.Close()
		return report, nil
	}
	return report, puncher
}

func probeSTUNWithOptions(
	ctx context.Context,
	urls []string,
	timeout time.Duration,
	opts STUNOptions,
) (STUNReport, *net.UDPConn) {
	report := STUNReport{Class: NATUnknown}
	if timeout <= 0 {
		report.Error = "STUN timeout must be positive"
		return report, nil
	}
	endpoints := parseSTUNEndpoints(urls)
	if len(endpoints) == 0 {
		report.Error = "no UDP STUN server configured"
		return report, nil
	}
	networks := []string{"udp4", "udp6"}
	if opts.IPv4 == nil && opts.IPv6 != nil {
		networks = []string{"udp6"}
	} else if opts.IPv4 != nil && opts.IPv6 == nil {
		networks = []string{"udp4"}
	}
	network, targets, err := resolveSTUNTargetsForNetworks(ctx, endpoints, networks)
	if err != nil {
		report.Error = err.Error()
		return report, nil
	}
	var localAddr *net.UDPAddr
	if network == "udp4" && opts.IPv4 != nil {
		localAddr = &net.UDPAddr{IP: append(net.IP(nil), opts.IPv4...)}
	} else if network == "udp6" && opts.IPv6 != nil {
		localAddr = &net.UDPAddr{IP: append(net.IP(nil), opts.IPv6...)}
	}
	conn, err := net.ListenUDP(network, localAddr)
	if err != nil {
		report.Error = fmt.Sprintf("open STUN socket: %v", err)
		return report, nil
	}
	report.Local = conn.LocalAddr().String()

	deadline := time.Now().Add(timeout)
	successful := make([]*net.UDPAddr, 0, maxSTUNServers)
	for index := 0; index < len(targets) && index < maxSTUNServers; index++ {
		target := targets[index]
		started := time.Now()
		result, probeErr := querySTUNBinding(ctx, conn, target.addr, remainingTimeout(deadline))
		observation := STUNObservation{
			Server:        target.raw,
			LatencyMillis: max(time.Since(started).Milliseconds(), int64(1)),
		}
		if probeErr != nil {
			observation.Error = probeErr.Error()
		} else {
			observation.Mapped = result.mapped.String()
			successful = append(successful, result.mapped)
			if len(targets) == 1 && result.other != nil && sameUDPFamily(network, result.other.IP) {
				targets = append(targets, stunTarget{
					raw:  "stun:" + result.other.String(),
					addr: result.other,
				})
			}
		}
		report.Observations = append(report.Observations, observation)
		if time.Until(deadline) <= 0 {
			break
		}
	}

	report.OK = len(successful) > 0
	localIPs := opts.InterfaceIPs
	if len(localIPs) == 0 {
		localIPs = localInterfaceIPs()
	}
	report.Class = classifyNAT(conn.LocalAddr(), localIPs, successful)
	if !report.OK {
		var failures []string
		for _, observation := range report.Observations {
			if observation.Error != "" {
				failures = append(failures, observation.Server+": "+observation.Error)
			}
		}
		if len(failures) == 0 {
			report.Error = "STUN probe returned no mapping"
		} else {
			report.Error = strings.Join(failures, "; ")
		}
	}
	return report, conn
}

func parseSTUNEndpoints(urls []string) []stunEndpoint {
	seen := map[string]struct{}{}
	out := make([]stunEndpoint, 0, maxSTUNServers)
	for _, raw := range urls {
		raw = strings.TrimSpace(raw)
		uri, err := stun.ParseURI(raw)
		if err != nil || uri.Scheme != stun.SchemeTypeSTUN || uri.Proto != stun.ProtoTypeUDP {
			continue
		}
		key := net.JoinHostPort(strings.ToLower(uri.Host), fmt.Sprint(uri.Port))
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, stunEndpoint{raw: raw, host: uri.Host, port: uri.Port})
		if len(out) == maxSTUNServers {
			break
		}
	}
	return out
}

func resolveSTUNTargets(ctx context.Context, endpoints []stunEndpoint) (string, []stunTarget, error) {
	return resolveSTUNTargetsForNetworks(ctx, endpoints, []string{"udp4", "udp6"})
}

func resolveSTUNTargetsForNetworks(ctx context.Context, endpoints []stunEndpoint, networks []string) (string, []stunTarget, error) {
	type resolvedEndpoint struct {
		endpoint stunEndpoint
		ips      []net.IP
		err      error
	}
	resolved := make([]resolvedEndpoint, 0, len(endpoints))
	for _, endpoint := range endpoints {
		ips, err := resolveSTUNHost(ctx, endpoint.host)
		resolved = append(resolved, resolvedEndpoint{endpoint: endpoint, ips: ips, err: err})
	}
	for _, network := range networks {
		var targets []stunTarget
		for _, item := range resolved {
			ip := firstIPForNetwork(item.ips, network)
			if ip == nil {
				continue
			}
			targets = append(targets, stunTarget{
				raw: item.endpoint.raw,
				addr: &net.UDPAddr{
					IP:   ip,
					Port: item.endpoint.port,
				},
			})
		}
		if len(targets) == len(endpoints) || len(targets) >= maxSTUNServers {
			return network, targets, nil
		}
	}
	for _, network := range networks {
		for _, item := range resolved {
			if ip := firstIPForNetwork(item.ips, network); ip != nil {
				return network, []stunTarget{{
					raw: item.endpoint.raw,
					addr: &net.UDPAddr{
						IP:   ip,
						Port: item.endpoint.port,
					},
				}}, nil
			}
		}
	}
	var failures []string
	for _, item := range resolved {
		if item.err != nil {
			failures = append(failures, item.endpoint.host+": "+item.err.Error())
		}
	}
	if len(failures) == 0 {
		return "", nil, errors.New("STUN servers have no usable UDP addresses")
	}
	return "", nil, errors.New(strings.Join(failures, "; "))
}

func resolveSTUNHost(ctx context.Context, host string) ([]net.IP, error) {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}, nil
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	ips := make([]net.IP, 0, len(addresses))
	for _, address := range addresses {
		if address.IP != nil {
			ips = append(ips, address.IP)
		}
	}
	return ips, nil
}

func firstIPForNetwork(ips []net.IP, network string) net.IP {
	for _, ip := range ips {
		if network == "udp4" {
			if ip4 := ip.To4(); ip4 != nil {
				return append(net.IP(nil), ip4...)
			}
			continue
		}
		if ip.To4() == nil && ip.To16() != nil {
			return append(net.IP(nil), ip...)
		}
	}
	return nil
}

func querySTUNBinding(
	ctx context.Context,
	conn *net.UDPConn,
	target *net.UDPAddr,
	timeout time.Duration,
) (bindingResult, error) {
	if timeout <= 0 {
		return bindingResult{}, context.DeadlineExceeded
	}
	request, err := stun.Build(stun.TransactionID, stun.BindingRequest, stun.Fingerprint)
	if err != nil {
		return bindingResult{}, err
	}
	deadline := time.Now().Add(timeout)
	for attempt := 0; attempt < 2; attempt++ {
		if err := ctx.Err(); err != nil {
			return bindingResult{}, err
		}
		if _, err := conn.WriteToUDP(request.Raw, target); err != nil {
			return bindingResult{}, err
		}
		attemptDeadline := deadline
		if attempt == 0 {
			attemptDeadline = time.Now().Add(max(time.Until(deadline)/2, time.Millisecond))
		}
		if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(attemptDeadline) {
			attemptDeadline = contextDeadline
		}
		if err := conn.SetReadDeadline(attemptDeadline); err != nil {
			return bindingResult{}, err
		}
		for {
			buffer := make([]byte, 1500)
			n, _, readErr := conn.ReadFromUDP(buffer)
			if readErr != nil {
				var netErr net.Error
				if errors.As(readErr, &netErr) && netErr.Timeout() {
					break
				}
				if ctx.Err() != nil {
					return bindingResult{}, ctx.Err()
				}
				return bindingResult{}, readErr
			}
			message := &stun.Message{Raw: append([]byte(nil), buffer[:n]...)}
			if err := message.Decode(); err != nil ||
				message.TransactionID != request.TransactionID ||
				message.Type != stun.BindingSuccess {
				continue
			}
			var mapped stun.XORMappedAddress
			if err := mapped.GetFrom(message); err != nil {
				var legacy stun.MappedAddress
				if legacyErr := legacy.GetFrom(message); legacyErr != nil {
					return bindingResult{}, fmt.Errorf("STUN response has no mapped address")
				}
				mapped.IP = legacy.IP
				mapped.Port = legacy.Port
			}
			result := bindingResult{
				mapped: &net.UDPAddr{IP: append(net.IP(nil), mapped.IP...), Port: mapped.Port},
			}
			var other stun.OtherAddress
			if err := other.GetFrom(message); err == nil {
				result.other = &net.UDPAddr{
					IP:   append(net.IP(nil), other.IP...),
					Port: other.Port,
				}
			}
			return result, nil
		}
	}
	if ctx.Err() != nil {
		return bindingResult{}, ctx.Err()
	}
	return bindingResult{}, fmt.Errorf("STUN binding to %s timed out", target)
}

func classifyNAT(local net.Addr, localIPs []net.IP, mapped []*net.UDPAddr) NATClass {
	if len(mapped) == 0 {
		return NATUnknown
	}
	localUDP, _ := local.(*net.UDPAddr)
	isOpen := func(candidate *net.UDPAddr) bool {
		if localUDP == nil || candidate == nil || candidate.Port != localUDP.Port {
			return false
		}
		return slices.ContainsFunc(localIPs, func(ip net.IP) bool {
			return ip.Equal(candidate.IP)
		})
	}
	if len(mapped) == 1 {
		if isOpen(mapped[0]) {
			return NATOpen
		}
		return NATUnknown
	}
	if !mapped[0].IP.Equal(mapped[1].IP) || mapped[0].Port != mapped[1].Port {
		return NATSymmetric
	}
	if isOpen(mapped[0]) {
		return NATOpen
	}
	return NATCone
}

func localInterfaceIPs() []net.IP {
	var out []net.IP
	interfaces, err := net.Interfaces()
	if err != nil {
		return out
	}
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 {
			continue
		}
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			switch value := address.(type) {
			case *net.IPNet:
				out = append(out, append(net.IP(nil), value.IP...))
			case *net.IPAddr:
				out = append(out, append(net.IP(nil), value.IP...))
			}
		}
	}
	return out
}

func remainingTimeout(deadline time.Time) time.Duration {
	return max(time.Until(deadline), time.Millisecond)
}

func sameUDPFamily(network string, ip net.IP) bool {
	if network == "udp4" {
		return ip.To4() != nil
	}
	return ip.To4() == nil && ip.To16() != nil
}
