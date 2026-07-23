package app

import (
	"testing"
	"time"

	"github.com/suir1/kigo/internal/netprobe"
)

func TestAdaptiveDirectTimeout(t *testing.T) {
	tests := []struct {
		name  string
		local netprobe.NATClass
		peer  netprobe.NATClass
		want  time.Duration
	}{
		{
			name:  "both symmetric",
			local: netprobe.NATSymmetric,
			peer:  netprobe.NATSymmetric,
			want:  500 * time.Millisecond,
		},
		{
			name:  "one symmetric",
			local: netprobe.NATCone,
			peer:  netprobe.NATSymmetric,
			want:  700 * time.Millisecond,
		},
		{
			name:  "one open",
			local: netprobe.NATOpen,
			peer:  netprobe.NATUnknown,
			want:  3500 * time.Millisecond,
		},
		{
			name:  "both stable",
			local: netprobe.NATCone,
			peer:  netprobe.NATCone,
			want:  1500 * time.Millisecond,
		},
		{
			name:  "unknown",
			local: netprobe.NATUnknown,
			peer:  netprobe.NATUnknown,
			want:  900 * time.Millisecond,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, reason := adaptiveDirectTimeout(
				900*time.Millisecond,
				test.local,
				test.peer,
			)
			if got != test.want || reason == "" {
				t.Fatalf("timeout = %s, reason = %q", got, reason)
			}
		})
	}
}

func TestAdaptiveDirectTimeoutPreservesInvalidBase(t *testing.T) {
	got, _ := adaptiveDirectTimeout(
		0,
		netprobe.NATOpen,
		netprobe.NATOpen,
	)
	if got != 0 {
		t.Fatalf("timeout = %s", got)
	}
}

func TestDirectTimeoutRequiresBothPeersToOptIn(t *testing.T) {
	g := &globalOptions{
		UDPProbe:      true,
		DirectTimeout: 900 * time.Millisecond,
	}
	local := netprobe.STUNReport{OK: true, Class: netprobe.NATOpen}
	peer := directRendezvousResponse{
		PeerNATProbe: false,
		PeerNATClass: "symmetric",
	}
	got, reason := directTimeoutForPeer(g, local, peer)
	if got != g.DirectTimeout || reason != "" {
		t.Fatalf("timeout = %s, reason = %q", got, reason)
	}
}

func TestDirectNATClassHidesFailedProbe(t *testing.T) {
	if got := directNATClass(netprobe.STUNReport{
		Class: netprobe.NATSymmetric,
		Error: "unreachable",
	}); got != netprobe.NATUnknown {
		t.Fatalf("class = %q", got)
	}
}
