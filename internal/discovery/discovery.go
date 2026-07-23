package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/ipv4"
)

const (
	DefaultAddr       = "239.255.77.77:48765"
	protocolMagic     = "kigo-relay-v1"
	announceInterval  = 200 * time.Millisecond
	maxPacketSize     = 1024
	maxRelayPort      = 65535
	minDiscoveryDelay = 10 * time.Millisecond
)

type announcement struct {
	Magic string `json:"magic"`
	Port  int    `json:"port"`
}

func Announce(ctx context.Context, addr string, relayPort int) error {
	return AnnounceOnInterface(ctx, addr, relayPort, "")
}

func AnnounceOnInterface(ctx context.Context, addr string, relayPort int, interfaceName string) error {
	if relayPort <= 0 || relayPort > maxRelayPort {
		return fmt.Errorf("invalid relay announcement port %d", relayPort)
	}
	target, err := resolveAddr(addr)
	if err != nil {
		return err
	}
	iface, source, err := selectedIPv4Interface(interfaceName)
	if err != nil {
		return err
	}
	var local *net.UDPAddr
	if source != nil {
		local = &net.UDPAddr{IP: source}
	}
	conn, err := net.ListenUDP("udp4", local)
	if err != nil {
		return err
	}
	defer conn.Close()
	if iface != nil && target.IP.IsMulticast() {
		if err := ipv4.NewPacketConn(conn).SetMulticastInterface(iface); err != nil {
			return fmt.Errorf("set discovery multicast interface: %w", err)
		}
	}
	payload, err := json.Marshal(announcement{Magic: protocolMagic, Port: relayPort})
	if err != nil {
		return err
	}
	send := func() {
		_, _ = conn.WriteToUDP(payload, target)
	}
	send()
	ticker := time.NewTicker(announceInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			send()
		}
	}
}

func Discover(ctx context.Context, addr string, timeout time.Duration) ([]string, error) {
	return DiscoverOnInterface(ctx, addr, timeout, "")
}

func DiscoverOnInterface(ctx context.Context, addr string, timeout time.Duration, interfaceName string) ([]string, error) {
	if timeout <= 0 {
		return nil, errors.New("discovery timeout must be positive")
	}
	target, err := resolveAddr(addr)
	if err != nil {
		return nil, err
	}
	iface, _, err := selectedIPv4Interface(interfaceName)
	if err != nil {
		return nil, err
	}
	conn, err := listen(target, iface)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	deadline := time.Now().Add(timeout)
	if parentDeadline, ok := ctx.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return nil, err
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()

	seen := map[string]struct{}{}
	var endpoints []string
	buffer := make([]byte, maxPacketSize)
	for {
		n, source, err := conn.ReadFromUDP(buffer)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				return endpoints, nil
			}
			if errors.Is(err, net.ErrClosed) {
				return nil, ctx.Err()
			}
			return nil, err
		}
		port, ok := parseAnnouncement(buffer[:n])
		if !ok || source.IP == nil || source.IP.IsUnspecified() {
			continue
		}
		endpoint := net.JoinHostPort(source.IP.String(), strconv.Itoa(port))
		if _, ok := seen[endpoint]; ok {
			continue
		}
		seen[endpoint] = struct{}{}
		endpoints = append(endpoints, endpoint)
	}
}

func listen(target *net.UDPAddr, iface *net.Interface) (*net.UDPConn, error) {
	if target.IP.IsMulticast() {
		return net.ListenMulticastUDP("udp4", iface, target)
	}
	return net.ListenUDP("udp4", target)
}

func selectedIPv4Interface(name string) (*net.Interface, net.IP, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil, nil
	}
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return nil, nil, fmt.Errorf("discovery interface %q: %w", name, err)
	}
	addresses, err := iface.Addrs()
	if err != nil {
		return nil, nil, fmt.Errorf("discovery interface %q addresses: %w", name, err)
	}
	for _, address := range addresses {
		var ip net.IP
		switch value := address.(type) {
		case *net.IPNet:
			ip = value.IP
		case *net.IPAddr:
			ip = value.IP
		}
		if ip4 := ip.To4(); ip4 != nil {
			return iface, append(net.IP(nil), ip4...), nil
		}
	}
	return nil, nil, fmt.Errorf("discovery interface %q has no IPv4 address", name)
}

func resolveAddr(addr string) (*net.UDPAddr, error) {
	if addr == "" {
		addr = DefaultAddr
	}
	target, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		return nil, err
	}
	if target.IP == nil || target.Port <= 0 {
		return nil, fmt.Errorf("invalid discovery address %q", addr)
	}
	return target, nil
}

func parseAnnouncement(payload []byte) (int, bool) {
	var message announcement
	if err := json.Unmarshal(payload, &message); err != nil {
		return 0, false
	}
	if message.Magic != protocolMagic || message.Port <= 0 || message.Port > maxRelayPort {
		return 0, false
	}
	return message.Port, true
}
