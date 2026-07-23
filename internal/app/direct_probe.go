package app

import (
	"context"
	"errors"
	"net"
	"strconv"
	"time"

	"github.com/suir1/kigo/internal/directcandidate"
	"github.com/suir1/kigo/internal/netreuse"
	"github.com/suir1/kigo/internal/relay"
)

const directPublicProbeTimeout = 600 * time.Millisecond

func listenDirect(ctx context.Context, address string) (net.Listener, error) {
	return netreuse.ListenTCP(ctx, address)
}

func directListenerPort(listener net.Listener) int {
	if listener == nil {
		return 0
	}
	_, portText, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		return 0
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return 0
	}
	return port
}

func probeRelayObservedDirectCandidate(
	ctx context.Context,
	g *globalOptions,
	roomToken string,
	role string,
	listener net.Listener,
) (directcandidate.Candidate, bool) {
	candidate, err := probeRelayObservedDirectCandidateResult(ctx, g, roomToken, role, listener)
	return candidate, err == nil
}

func probeRelayObservedDirectCandidateResult(
	ctx context.Context,
	g *globalOptions,
	roomToken string,
	role string,
	listener net.Listener,
) (directcandidate.Candidate, error) {
	if !tcpPublicProbeEnabled(g) {
		return directcandidate.Candidate{}, errors.New("TCP public probe is disabled or has no relay")
	}
	localPort := directListenerPort(listener)
	if localPort == 0 {
		return directcandidate.Candidate{}, errors.New("direct listener has no usable TCP port")
	}
	probeCtx, cancel := context.WithTimeout(ctx, directPublicProbeTimeout)
	defer cancel()
	network := "tcp"
	var sourceIP net.IP
	if policy := selectedNetworkPolicy(g); policy != nil {
		sourceNetwork, source, err := policy.TCPAddrFor(g.Relay, localPort)
		if err != nil {
			return directcandidate.Candidate{}, err
		}
		network = sourceNetwork
		sourceIP = source.IP
	}
	address, err := relay.ProbePublicAddress(probeCtx, relay.PublicProbeOptions{
		Addr:      g.Relay,
		RoomToken: roomToken,
		Role:      role,
		Pass:      relayPass(g),
		LocalPort: localPort,
		Network:   network,
		SourceIP:  sourceIP,
	})
	if err != nil {
		return directcandidate.Candidate{}, err
	}
	candidate, err := directcandidate.FromRelayObservation(address)
	if err != nil {
		return directcandidate.Candidate{}, err
	}
	return candidate, nil
}

func tcpPublicProbeEnabled(g *globalOptions) bool {
	if g == nil || nativeDirectDisabled(g) || g.NoTCPProbe || g.Relay == "" || g.DirectAdvertise != "" {
		return false
	}
	return true
}

func appendDirectCandidate(
	addresses []string,
	metadata []directcandidate.Candidate,
	candidate directcandidate.Candidate,
) ([]string, []directcandidate.Candidate) {
	if directcandidate.Validate(candidate) != nil {
		return addresses, metadata
	}
	for _, address := range addresses {
		if address == candidate.Address {
			return addresses, metadata
		}
	}
	if len(addresses) >= directcandidate.MaxCandidates {
		return addresses, metadata
	}
	addresses = append(append([]string(nil), addresses...), candidate.Address)
	metadata = append(append([]directcandidate.Candidate(nil), metadata...), candidate)
	merged := directcandidate.Merge(addresses, metadata)
	return directcandidate.Addresses(merged), merged
}
