package app

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/suir1/kigo/internal/directcandidate"
	"github.com/suir1/kigo/internal/netreuse"
	"github.com/suir1/kigo/internal/relay"
)

func TestAppendDirectCandidateKeepsPublicBehindLAN(t *testing.T) {
	addresses, metadata := appendDirectCandidate(
		[]string{"192.168.1.2:4000"},
		[]directcandidate.Candidate{{
			Address:  "192.168.1.2:4000",
			Kind:     directcandidate.KindLAN,
			Priority: directcandidate.PriorityLAN,
		}},
		directcandidate.Candidate{
			Address:  "203.0.113.2:5000",
			Kind:     directcandidate.KindPublic,
			Priority: directcandidate.PriorityPublic,
		},
	)
	if len(addresses) != 2 || len(metadata) != 2 {
		t.Fatalf("addresses=%#v metadata=%#v", addresses, metadata)
	}
	if metadata[0].Kind != directcandidate.KindLAN ||
		metadata[1].Kind != directcandidate.KindPublic {
		t.Fatalf("metadata = %#v", metadata)
	}
}

func TestTCPPublicProbeEnablement(t *testing.T) {
	if tcpPublicProbeEnabled(nil) {
		t.Fatal("nil options enabled TCP probe")
	}
	if tcpPublicProbeEnabled(&globalOptions{Relay: "relay.example:9000", NoTCPProbe: true}) {
		t.Fatal("--no-tcp-probe enabled TCP probe")
	}
	if tcpPublicProbeEnabled(&globalOptions{
		Relay:           "relay.example:9000",
		DirectAdvertise: "127.0.0.1:4000",
	}) {
		t.Fatal("explicit direct advertise enabled TCP probe")
	}
	if !tcpPublicProbeEnabled(&globalOptions{Relay: "relay.example:9000"}) {
		t.Fatal("configured relay did not enable TCP probe")
	}
}

func TestInspectDirectReportsRelayObservedMapping(t *testing.T) {
	if !netreuse.Supported {
		t.Skip("same-port socket reuse is unsupported on this platform")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	relayListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := relay.NewServer().Serve(ctx, relayListener); err != nil {
			t.Errorf("relay serve failed: %v", err)
		}
	}()
	report := inspectDirect(ctx, &globalOptions{
		Relay:         relayListener.Addr().String(),
		DirectListen:  "127.0.0.1:0",
		DirectTimeout: time.Second,
	})
	if !report.OK || !report.TCPProbeEnabled || report.PublicAddress == "" ||
		report.PublicProbeError != "" || !report.SamePortSupported {
		t.Fatalf("direct report = %#v", report)
	}
}

func TestInspectDirectReportsSamePortPlatformSupport(t *testing.T) {
	report := inspectDirect(context.Background(), &globalOptions{
		DirectListen:  "127.0.0.1:0",
		DirectTimeout: time.Second,
	})
	if !report.OK {
		t.Fatalf("direct report = %#v", report)
	}
	if report.SamePortSupported != netreuse.Supported {
		t.Fatalf(
			"same-port support = %t, want %t",
			report.SamePortSupported,
			netreuse.Supported,
		)
	}
}
