package netprobe

import (
	"context"
	"net"
	"slices"
	"testing"
	"time"
)

func TestProbeSTUNForPunchKeepsProbedSocketAndCandidates(t *testing.T) {
	server := newTestSTUNServer(t, func(remote *net.UDPAddr) *net.UDPAddr {
		return remote
	}, nil)
	loopback := net.ParseIP("127.0.0.1")
	report, puncher := ProbeSTUNForPunch(
		context.Background(),
		[]string{server.URL()},
		time.Second,
		STUNOptions{IPv4: loopback, InterfaceIPs: []net.IP{loopback}},
	)
	if puncher == nil {
		t.Fatal("puncher is nil")
	}
	defer puncher.Close()
	if !report.OK || !slices.Contains(puncher.Candidates(), report.Local) {
		t.Fatalf("report=%#v candidates=%#v", report, puncher.Candidates())
	}
	if puncher.conn.LocalAddr().String() != report.Local {
		t.Fatalf("live socket=%q report local=%q", puncher.conn.LocalAddr(), report.Local)
	}
}

func TestUDPPunchersExchangeAuthenticatedPackets(t *testing.T) {
	left := testUDPPuncher(t)
	right := testUDPPuncher(t)
	punchAt := time.Now().Add(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	type outcome struct {
		result UDPPunchResult
		err    error
	}
	results := make(chan outcome, 2)
	go func() {
		result, err := left.Punch(ctx, right.Candidates(), "room-token", "sender", punchAt)
		results <- outcome{result: result, err: err}
	}()
	go func() {
		result, err := right.Punch(ctx, left.Candidates(), "room-token", "receiver", punchAt)
		results <- outcome{result: result, err: err}
	}()
	for range 2 {
		outcome := <-results
		if outcome.err != nil || !outcome.result.Received {
			t.Fatalf("outcome=%#v", outcome)
		}
	}
}

func TestUDPPunchRejectsWrongRoomToken(t *testing.T) {
	packet := makeUDPPunchPacket("room-a", "sender", 1234)
	if validUDPPunchPacket(packet, "room-b", "sender", 1234) {
		t.Fatal("packet authenticated with the wrong room token")
	}
	if validUDPPunchPacket(packet, "room-a", "receiver", 1234) {
		t.Fatal("packet authenticated with the wrong role")
	}
	if validUDPPunchPacket(packet, "room-a", "sender", 1235) {
		t.Fatal("packet authenticated with the wrong punch time")
	}
}

func TestUDPPunchKeepsAgreedTimestampAfterLateStart(t *testing.T) {
	left := testUDPPuncher(t)
	right := testUDPPuncher(t)
	punchAt := time.Now().Add(-20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	results := make(chan UDPPunchResult, 2)
	go func() {
		result, _ := left.Punch(ctx, right.Candidates(), "late-room", "sender", punchAt)
		results <- result
	}()
	go func() {
		result, _ := right.Punch(ctx, left.Candidates(), "late-room", "receiver", punchAt)
		results <- result
	}()
	for range 2 {
		if result := <-results; !result.Received {
			t.Fatalf("late punch result=%#v", result)
		}
	}
}

func testUDPPuncher(t *testing.T) *UDPPuncher {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	puncher := &UDPPuncher{conn: conn, candidates: []string{conn.LocalAddr().String()}}
	t.Cleanup(func() { _ = puncher.Close() })
	return puncher
}
