package app

import (
	"context"
	"time"

	"github.com/suir1/kigo/internal/netprobe"
)

const (
	directNATProbeBudget = 1500 * time.Millisecond
	directSTUNTimeout    = 800 * time.Millisecond
)

func prepareDirectUDP(ctx context.Context, g *globalOptions) (netprobe.STUNReport, *netprobe.UDPPuncher) {
	if g == nil || !g.UDPProbe {
		return netprobe.STUNReport{Class: netprobe.NATUnknown}, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, directNATProbeBudget)
	defer cancel()
	servers, err := fetchICEServersWithOptions(probeCtx, g.Signal, g)
	if err != nil {
		report := netprobe.STUNReport{
			Class: netprobe.NATUnknown,
			Error: err.Error(),
		}
		taskLinef(g, "UDP NAT probe unavailable: %v", err)
		return report, nil
	}
	var urls []string
	for _, server := range servers {
		urls = append(urls, server.URLs...)
	}
	var puncher *netprobe.UDPPuncher
	report, puncher := netprobe.ProbeSTUNForPunch(
		probeCtx,
		urls,
		directSTUNTimeout,
		outboundSTUNOptions(g),
	)
	if !report.OK {
		taskLinef(g, "UDP NAT probe unavailable: %s", report.Error)
		return report, puncher
	}
	taskLinef(g, "UDP NAT probe: %s", report.Class)
	return report, puncher
}

func udpPunchCandidates(puncher *netprobe.UDPPuncher) []string {
	if puncher == nil {
		return nil
	}
	return puncher.Candidates()
}

func startDirectUDPPunch(
	ctx context.Context,
	g *globalOptions,
	puncher *netprobe.UDPPuncher,
	peerCandidates []string,
	roomToken string,
	role string,
	punchAt time.Time,
) {
	if puncher == nil || len(peerCandidates) == 0 || punchAt.IsZero() {
		return
	}
	go func() {
		result, err := puncher.Punch(ctx, peerCandidates, roomToken, role, punchAt)
		if err == nil && result.Received {
			taskLinef(g, "UDP punch: authenticated peer reached via %s", result.Peer)
		}
	}()
}

func directNATClass(report netprobe.STUNReport) netprobe.NATClass {
	if report.OK && netprobe.ValidNATClass(string(report.Class)) {
		return report.Class
	}
	return netprobe.NATUnknown
}

func directTimeoutForPeer(
	g *globalOptions,
	local netprobe.STUNReport,
	peer directRendezvousResponse,
) (time.Duration, string) {
	if g == nil {
		return 0, ""
	}
	if !g.UDPProbe || !peer.PeerNATProbe {
		return g.DirectTimeout, ""
	}
	localClass := directNATClass(local)
	peerClass := netprobe.NATClass(peer.PeerNATClass)
	if !netprobe.ValidNATClass(string(peerClass)) {
		peerClass = netprobe.NATUnknown
	}
	return adaptiveDirectTimeout(g.DirectTimeout, localClass, peerClass)
}

func adaptiveDirectTimeout(
	base time.Duration,
	local netprobe.NATClass,
	peer netprobe.NATClass,
) (time.Duration, string) {
	if base <= 0 {
		return base, "configured direct timeout is invalid"
	}
	switch {
	case local == netprobe.NATSymmetric && peer == netprobe.NATSymmetric:
		return min(base, 500*time.Millisecond), "both peers reported destination-dependent UDP mappings"
	case local == netprobe.NATSymmetric || peer == netprobe.NATSymmetric:
		return min(base, 700*time.Millisecond), "one peer reported a destination-dependent UDP mapping"
	case local == netprobe.NATOpen || peer == netprobe.NATOpen:
		return max(base, 3500*time.Millisecond), "an openly reachable UDP mapping justifies a longer direct probe"
	case local == netprobe.NATCone && peer == netprobe.NATCone:
		return max(base, 1500*time.Millisecond), "both peers reported stable UDP mappings"
	default:
		return base, "NAT mapping is unknown; using configured direct timeout"
	}
}
