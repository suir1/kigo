package service

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

func parseTrustedProxies(value string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for _, raw := range strings.Split(value, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			addr, addrErr := netip.ParseAddr(raw)
			if addrErr != nil || addr.Zone() != "" {
				return nil, fmt.Errorf("invalid trusted proxy %q", raw)
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		if !prefix.IsValid() || prefix.Addr().Zone() != "" {
			return nil, fmt.Errorf("invalid trusted proxy %q", raw)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

func (s *Server) clientAddress(r *http.Request) string {
	remote, ok := parseAddress(r.RemoteAddr)
	if !ok || !s.isTrustedProxy(remote) {
		return r.RemoteAddr
	}

	forwarded := strings.Join(r.Header.Values("X-Forwarded-For"), ",")
	if forwarded != "" {
		parts := strings.Split(forwarded, ",")
		var leftmost netip.Addr
		for i := len(parts) - 1; i >= 0; i-- {
			addr, valid := parseAddress(strings.TrimSpace(parts[i]))
			if !valid {
				return remote.String()
			}
			leftmost = addr
			if !s.isTrustedProxy(addr) {
				return addr.String()
			}
		}
		if leftmost.IsValid() {
			return leftmost.String()
		}
	}

	if realIP, valid := parseAddress(strings.TrimSpace(r.Header.Get("X-Real-IP"))); valid {
		return realIP.String()
	}
	return remote.String()
}

func (s *Server) isTrustedProxy(addr netip.Addr) bool {
	for _, prefix := range s.trustedProxies {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func parseAddress(value string) (netip.Addr, bool) {
	value = strings.TrimSpace(value)
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	value = strings.Trim(value, "[]")
	addr, err := netip.ParseAddr(value)
	if err != nil || addr.Zone() != "" {
		return netip.Addr{}, false
	}
	return addr.Unmap(), true
}
