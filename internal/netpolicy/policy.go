package netpolicy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
)

// Policy constrains client sockets and ICE candidates to one local interface.
// Binding by source IP keeps the implementation portable across supported OSes.
type Policy struct {
	interfaceName string
	ips           []net.IP
	ipv4          net.IP
	ipv6          net.IP
}

// Resolve validates an interface name and captures its usable unicast addresses.
// An empty name returns a nil policy, which means normal OS routing.
func Resolve(name string) (*Policy, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, fmt.Errorf("network interface %q: %w", name, err)
	}
	if iface.Flags&net.FlagUp == 0 {
		return nil, fmt.Errorf("network interface %q is down", name)
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return nil, fmt.Errorf("network interface %q addresses: %w", name, err)
	}
	policy := &Policy{interfaceName: iface.Name}
	for _, address := range addresses {
		ip := addressIP(address)
		if !usableIP(ip) || policy.ContainsIP(ip) {
			continue
		}
		if ip4 := ip.To4(); ip4 != nil {
			ip = append(net.IP(nil), ip4...)
			if policy.ipv4 == nil {
				policy.ipv4 = append(net.IP(nil), ip...)
			}
		} else {
			ip = append(net.IP(nil), ip...)
			if policy.ipv6 == nil {
				policy.ipv6 = append(net.IP(nil), ip...)
			}
		}
		policy.ips = append(policy.ips, ip)
	}
	if len(policy.ips) == 0 {
		return nil, fmt.Errorf("network interface %q has no usable unicast address", name)
	}
	return policy, nil
}

func (p *Policy) InterfaceName() string {
	if p == nil {
		return ""
	}
	return p.interfaceName
}

func (p *Policy) IPs() []net.IP {
	if p == nil {
		return nil
	}
	out := make([]net.IP, len(p.ips))
	for index, ip := range p.ips {
		out[index] = append(net.IP(nil), ip...)
	}
	return out
}

func (p *Policy) IPv4() net.IP {
	if p == nil || p.ipv4 == nil {
		return nil
	}
	return append(net.IP(nil), p.ipv4...)
}

func (p *Policy) IPv6() net.IP {
	if p == nil || p.ipv6 == nil {
		return nil
	}
	return append(net.IP(nil), p.ipv6...)
}

func (p *Policy) ContainsIP(ip net.IP) bool {
	if p == nil || ip == nil {
		return false
	}
	for _, candidate := range p.ips {
		if candidate.Equal(ip) {
			return true
		}
	}
	return false
}

func (p *Policy) InterfaceFilter(name string) bool {
	return p == nil || name == p.interfaceName
}

func (p *Policy) IPFilter(ip net.IP) bool {
	return p == nil || p.ContainsIP(ip)
}

// UDPAddr returns a source address for a concrete UDP address family.
func (p *Policy) UDPAddr(network string) (*net.UDPAddr, error) {
	if p == nil {
		return nil, nil
	}
	ip, _, err := p.ipForNetwork(network)
	if err != nil {
		return nil, err
	}
	return &net.UDPAddr{IP: ip}, nil
}

// TCPAddrFor selects a source address and concrete family for a direct target.
// Hostnames prefer IPv4 when the selected interface has both families.
func (p *Policy) TCPAddrFor(address string, port int) (string, *net.TCPAddr, error) {
	if p == nil {
		return "tcp", &net.TCPAddr{Port: port}, nil
	}
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return "", nil, fmt.Errorf("network target %q: %w", address, err)
	}
	host = strings.Trim(host, "[]")
	if zone := strings.LastIndexByte(host, '%'); zone >= 0 {
		host = host[:zone]
	}
	if remote := net.ParseIP(host); remote != nil {
		network := "tcp6"
		if remote.To4() != nil {
			network = "tcp4"
		}
		ip, family, err := p.ipForNetwork(network)
		if err != nil {
			return "", nil, err
		}
		return family, &net.TCPAddr{IP: ip, Port: port}, nil
	}
	ip, family, err := p.preferredIP("tcp")
	if err != nil {
		return "", nil, err
	}
	return family, &net.TCPAddr{IP: ip, Port: port}, nil
}

// DialContext binds TCP/UDP connections to an address on the selected interface.
func (p *Policy) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if p == nil {
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, address)
	}
	if !strings.HasPrefix(network, "tcp") && !strings.HasPrefix(network, "udp") {
		return nil, fmt.Errorf("network interface policy does not support %q", network)
	}
	families, err := p.dialFamilies(network)
	if err != nil {
		return nil, err
	}
	dialCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		conn net.Conn
		err  error
	}
	results := make(chan result, len(families))
	for _, family := range families {
		family := family
		go func() {
			ip, _, selectErr := p.ipForNetwork(family)
			if selectErr != nil {
				results <- result{err: selectErr}
				return
			}
			dialer := net.Dialer{}
			if strings.HasPrefix(family, "tcp") {
				dialer.LocalAddr = &net.TCPAddr{IP: ip}
			} else {
				dialer.LocalAddr = &net.UDPAddr{IP: ip}
			}
			conn, dialErr := dialer.DialContext(dialCtx, family, address)
			results <- result{conn: conn, err: dialErr}
		}()
	}
	var failures []string
	for range families {
		result := <-results
		if result.err == nil {
			cancel()
			return result.conn, nil
		}
		failures = append(failures, result.err.Error())
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("dial through interface %s: %s", p.interfaceName, strings.Join(failures, "; "))
}

func (p *Policy) dialFamilies(network string) ([]string, error) {
	prefix := "tcp"
	if strings.HasPrefix(network, "udp") {
		prefix = "udp"
	}
	switch network {
	case "tcp4", "udp4":
		if p.ipv4 == nil {
			return nil, fmt.Errorf("network interface %s has no IPv4 address", p.interfaceName)
		}
		return []string{prefix + "4"}, nil
	case "tcp6", "udp6":
		if p.ipv6 == nil {
			return nil, fmt.Errorf("network interface %s has no IPv6 address", p.interfaceName)
		}
		return []string{prefix + "6"}, nil
	case "tcp", "udp":
		var families []string
		if p.ipv4 != nil {
			families = append(families, prefix+"4")
		}
		if p.ipv6 != nil {
			families = append(families, prefix+"6")
		}
		return families, nil
	default:
		return nil, fmt.Errorf("unsupported network %q", network)
	}
}

func (p *Policy) preferredIP(network string) (net.IP, string, error) {
	if p.ipv4 != nil {
		return append(net.IP(nil), p.ipv4...), network + "4", nil
	}
	if p.ipv6 != nil {
		return append(net.IP(nil), p.ipv6...), network + "6", nil
	}
	return nil, "", errors.New("selected network interface has no usable address")
}

func (p *Policy) ipForNetwork(network string) (net.IP, string, error) {
	if strings.HasSuffix(network, "4") {
		if p.ipv4 == nil {
			return nil, "", fmt.Errorf("network interface %s has no IPv4 address", p.interfaceName)
		}
		return append(net.IP(nil), p.ipv4...), strings.TrimSuffix(network, "4") + "4", nil
	}
	if strings.HasSuffix(network, "6") {
		if p.ipv6 == nil {
			return nil, "", fmt.Errorf("network interface %s has no IPv6 address", p.interfaceName)
		}
		return append(net.IP(nil), p.ipv6...), strings.TrimSuffix(network, "6") + "6", nil
	}
	return p.preferredIP(strings.TrimRight(network, "46"))
}

func addressIP(address net.Addr) net.IP {
	switch value := address.(type) {
	case *net.IPNet:
		return value.IP
	case *net.IPAddr:
		return value.IP
	default:
		return nil
	}
}

func usableIP(ip net.IP) bool {
	return ip != nil && !ip.IsUnspecified() && !ip.IsMulticast() && !ip.IsLinkLocalUnicast()
}
