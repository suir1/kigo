package relay

import (
	"context"
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suir1/kigo/internal/directcandidate"
	"github.com/suir1/kigo/internal/netreuse"
	"github.com/suir1/kigo/internal/transport"
)

func TestRelayPairsAndForwardsFrames(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := NewServer().Serve(ctx, ln); err != nil {
			t.Errorf("relay serve failed: %v", err)
		}
	}()

	senderCh := make(chan result, 1)
	receiverCh := make(chan result, 1)
	go func() {
		tp, err := Join(ctx, ln.Addr().String(), "room-token", "sender", "")
		senderCh <- result{tp: tp, err: err}
	}()
	go func() {
		tp, err := Join(ctx, ln.Addr().String(), "room-token", "receiver", "")
		receiverCh <- result{tp: tp, err: err}
	}()

	sender := (<-senderCh).must(t)
	receiver := (<-receiverCh).must(t)
	defer sender.Close()
	defer receiver.Close()

	if err := sender.Send(ctx, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	got, err := receiver.Recv(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestRelayExchangesPeerDirectAddress(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := NewServer().Serve(ctx, ln); err != nil {
			t.Errorf("relay serve failed: %v", err)
		}
	}()

	senderCh := make(chan struct {
		result JoinResult
		err    error
	}, 1)
	receiverCh := make(chan struct {
		result JoinResult
		err    error
	}, 1)
	go func() {
		result, err := JoinWithOptions(ctx, JoinOptions{Addr: ln.Addr().String(), RoomToken: "room-token", Role: "sender", Direct: "127.0.0.1:1111"})
		senderCh <- struct {
			result JoinResult
			err    error
		}{result: result, err: err}
	}()
	go func() {
		result, err := JoinWithOptions(ctx, JoinOptions{Addr: ln.Addr().String(), RoomToken: "room-token", Role: "receiver", Direct: "127.0.0.1:2222"})
		receiverCh <- struct {
			result JoinResult
			err    error
		}{result: result, err: err}
	}()

	sender := <-senderCh
	if sender.err != nil {
		t.Fatal(sender.err)
	}
	defer sender.result.Transport.Close()
	receiver := <-receiverCh
	if receiver.err != nil {
		t.Fatal(receiver.err)
	}
	defer receiver.result.Transport.Close()
	if sender.result.PeerDirect != "127.0.0.1:2222" {
		t.Fatalf("sender peer direct = %q", sender.result.PeerDirect)
	}
	if receiver.result.PeerDirect != "127.0.0.1:1111" {
		t.Fatalf("receiver peer direct = %q", receiver.result.PeerDirect)
	}
}

func TestRelayExchangesPeerDirectCandidates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := NewServer().Serve(ctx, ln); err != nil {
			t.Errorf("relay serve failed: %v", err)
		}
	}()

	type joinResult struct {
		result JoinResult
		err    error
	}
	senderCh := make(chan joinResult, 1)
	go func() {
		result, err := JoinWithOptions(ctx, JoinOptions{
			Addr:             ln.Addr().String(),
			RoomToken:        "candidate-room",
			Role:             "sender",
			DirectCandidates: []string{"10.0.0.2:1111", "[fd00::2]:1111", "invalid"},
			DirectCandidateMetadata: []directcandidate.Candidate{{
				Address:  "10.0.0.2:1111",
				Kind:     directcandidate.KindLAN,
				Priority: directcandidate.PriorityLAN,
			}},
		})
		senderCh <- joinResult{result: result, err: err}
	}()
	receiver, err := JoinWithOptions(ctx, JoinOptions{
		Addr:             ln.Addr().String(),
		RoomToken:        "candidate-room",
		Role:             "receiver",
		DirectCandidates: []string{"10.0.0.3:2222"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Transport.Close()
	sender := <-senderCh
	if sender.err != nil {
		t.Fatal(sender.err)
	}
	defer sender.result.Transport.Close()

	if len(sender.result.PeerDirectCandidates) != 1 || sender.result.PeerDirectCandidates[0] != "10.0.0.3:2222" {
		t.Fatalf("sender candidates = %#v", sender.result.PeerDirectCandidates)
	}
	if len(receiver.PeerDirectCandidates) != 2 {
		t.Fatalf("receiver candidates = %#v", receiver.PeerDirectCandidates)
	}
	if len(receiver.PeerDirectCandidateMetadata) != 1 ||
		receiver.PeerDirectCandidateMetadata[0].Kind != directcandidate.KindLAN {
		t.Fatalf("receiver candidate metadata = %#v", receiver.PeerDirectCandidateMetadata)
	}
}

func TestProbePublicAddressUsesDirectListenerPort(t *testing.T) {
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
		if err := NewServer().Serve(ctx, relayListener); err != nil {
			t.Errorf("relay serve failed: %v", err)
		}
	}()
	directListener, err := netreuse.ListenTCP(ctx, "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer directListener.Close()
	directPort, err := listenerPort(directListener.Addr())
	if err != nil {
		t.Fatal(err)
	}
	publicAddress, err := ProbePublicAddress(ctx, PublicProbeOptions{
		Addr:      relayListener.Addr().String(),
		RoomToken: "public-probe-room",
		Role:      "sender",
		LocalPort: directPort,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, publicPort, err := net.SplitHostPort(publicAddress)
	if err != nil {
		t.Fatal(err)
	}
	if publicPort != strconv.Itoa(directPort) {
		t.Fatalf("public address = %q, want port %d", publicAddress, directPort)
	}
}

func TestRelayExchangesRouteChoiceCapabilityAndPreference(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := NewServer().Serve(ctx, ln); err != nil {
			t.Errorf("relay serve failed: %v", err)
		}
	}()

	type joinResult struct {
		result JoinResult
		err    error
	}
	senderCh := make(chan joinResult, 1)
	go func() {
		result, err := JoinWithOptions(ctx, JoinOptions{
			Addr:             ln.Addr().String(),
			RoomToken:        "route-choice-room",
			Role:             "sender",
			Capabilities:     []string{CapabilityRouteChoiceV1, CapabilityRouteChoiceV1},
			DirectPreference: DirectPreferenceRelay,
		})
		senderCh <- joinResult{result: result, err: err}
	}()
	receiver, err := JoinWithOptions(ctx, JoinOptions{
		Addr:             ln.Addr().String(),
		RoomToken:        "route-choice-room",
		Role:             "receiver",
		Capabilities:     []string{CapabilityRouteChoiceV1},
		DirectPreference: DirectPreferencePrefer,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Transport.Close()
	sender := <-senderCh
	if sender.err != nil {
		t.Fatal(sender.err)
	}
	defer sender.result.Transport.Close()

	if !hasCapabilityForTest(sender.result.PeerCapabilities, CapabilityRouteChoiceV1) {
		t.Fatalf("sender peer capabilities = %#v", sender.result.PeerCapabilities)
	}
	if sender.result.PeerDirectPreference != DirectPreferencePrefer {
		t.Fatalf("sender peer preference = %q", sender.result.PeerDirectPreference)
	}
	if !hasCapabilityForTest(receiver.PeerCapabilities, CapabilityRouteChoiceV1) {
		t.Fatalf("receiver peer capabilities = %#v", receiver.PeerCapabilities)
	}
	if receiver.PeerDirectPreference != DirectPreferenceRelay {
		t.Fatalf("receiver peer preference = %q", receiver.PeerDirectPreference)
	}
	if len(receiver.PeerCapabilities) != 1 {
		t.Fatalf("receiver peer capabilities were not normalized: %#v", receiver.PeerCapabilities)
	}
}

func TestRelaySynchronizesBidirectionalDirectPunch(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := NewServer().Serve(ctx, ln); err != nil {
			t.Errorf("relay serve failed: %v", err)
		}
	}()

	type joinResult struct {
		result JoinResult
		err    error
	}
	senderCh := make(chan joinResult, 1)
	go func() {
		result, err := JoinWithOptions(ctx, JoinOptions{
			Addr:             ln.Addr().String(),
			RoomToken:        "bidirectional-punch-room",
			Role:             "sender",
			DirectCandidates: []string{"127.0.0.1:4101"},
			UDPCandidates:    []string{"127.0.0.1:5101"},
			Capabilities: []string{
				CapabilityBidirectionalDirectV1,
				CapabilityUDPPunchV1,
			},
		})
		senderCh <- joinResult{result: result, err: err}
	}()
	receiver, err := JoinWithOptions(ctx, JoinOptions{
		Addr:             ln.Addr().String(),
		RoomToken:        "bidirectional-punch-room",
		Role:             "receiver",
		DirectCandidates: []string{"127.0.0.1:4102"},
		UDPCandidates:    []string{"127.0.0.1:5102"},
		Capabilities: []string{
			CapabilityBidirectionalDirectV1,
			CapabilityUDPPunchV1,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Transport.Close()
	sender := <-senderCh
	if sender.err != nil {
		t.Fatal(sender.err)
	}
	defer sender.result.Transport.Close()

	if sender.result.PunchAtMillis <= 0 {
		t.Fatalf("sender punch time = %d", sender.result.PunchAtMillis)
	}
	if receiver.PunchAtMillis != sender.result.PunchAtMillis {
		t.Fatalf(
			"sender punch time=%d receiver punch time=%d",
			sender.result.PunchAtMillis,
			receiver.PunchAtMillis,
		)
	}
	if !hasCapabilityForTest(sender.result.PeerCapabilities, CapabilityUDPPunchV1) ||
		len(sender.result.PeerUDPCandidates) != 1 || sender.result.PeerUDPCandidates[0] != "127.0.0.1:5102" ||
		!hasCapabilityForTest(receiver.PeerCapabilities, CapabilityUDPPunchV1) ||
		len(receiver.PeerUDPCandidates) != 1 || receiver.PeerUDPCandidates[0] != "127.0.0.1:5101" {
		t.Fatalf("UDP metadata sender=%#v receiver=%#v", sender.result, receiver)
	}
}

func TestRelayOmitsBidirectionalPunchForLegacyPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := NewServer().Serve(ctx, ln); err != nil {
			t.Errorf("relay serve failed: %v", err)
		}
	}()

	type joinResult struct {
		result JoinResult
		err    error
	}
	senderCh := make(chan joinResult, 1)
	go func() {
		result, err := JoinWithOptions(ctx, JoinOptions{
			Addr:             ln.Addr().String(),
			RoomToken:        "legacy-punch-room",
			Role:             "sender",
			DirectCandidates: []string{"127.0.0.1:4201"},
			UDPCandidates:    []string{"127.0.0.1:5201"},
			Capabilities: []string{
				CapabilityBidirectionalDirectV1,
				CapabilityUDPPunchV1,
			},
		})
		senderCh <- joinResult{result: result, err: err}
	}()
	receiver, err := JoinWithOptions(ctx, JoinOptions{
		Addr:             ln.Addr().String(),
		RoomToken:        "legacy-punch-room",
		Role:             "receiver",
		DirectCandidates: []string{"127.0.0.1:4202"},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Transport.Close()
	sender := <-senderCh
	if sender.err != nil {
		t.Fatal(sender.err)
	}
	defer sender.result.Transport.Close()

	if sender.result.PunchAtMillis != 0 || receiver.PunchAtMillis != 0 {
		t.Fatalf(
			"legacy punch time sender=%d receiver=%d",
			sender.result.PunchAtMillis,
			receiver.PunchAtMillis,
		)
	}
	if len(sender.result.PeerUDPCandidates) != 0 ||
		hasCapabilityForTest(sender.result.PeerCapabilities, CapabilityUDPPunchV1) {
		t.Fatalf("legacy peer unexpectedly enabled UDP punch: %#v", sender.result)
	}
}

func hasCapabilityForTest(capabilities []string, target string) bool {
	for _, capability := range capabilities {
		if capability == target {
			return true
		}
	}
	return false
}

func TestRelayRejectsDuplicateWaitingRole(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := NewServer().Serve(ctx, ln); err != nil {
			t.Errorf("relay serve failed: %v", err)
		}
	}()

	firstCh := make(chan error, 1)
	go func() {
		tp, err := Join(ctx, ln.Addr().String(), "room-token", "sender", "")
		if err == nil {
			defer tp.Close()
		}
		firstCh <- err
	}()
	time.Sleep(100 * time.Millisecond)

	if _, err := Join(ctx, ln.Addr().String(), "room-token", "sender", ""); err == nil {
		t.Fatal("duplicate sender joined")
	}

	receiver, err := Join(ctx, ln.Addr().String(), "room-token", "receiver", "")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	if err := <-firstCh; err != nil {
		t.Fatal(err)
	}
}

func TestRelayPairsConnectionsByIndex(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := NewServer().Serve(ctx, ln); err != nil {
			t.Errorf("relay serve failed: %v", err)
		}
	}()

	type indexedResult struct {
		index  int
		result JoinResult
		err    error
	}
	results := make(chan indexedResult, 4)
	for _, role := range []string{"sender", "receiver"} {
		for index := range 2 {
			role, index := role, index
			go func() {
				result, err := JoinWithOptions(ctx, JoinOptions{
					Addr:            ln.Addr().String(),
					RoomToken:       "indexed-room",
					Role:            role,
					ConnectionIndex: index,
					ConnectionCount: 2,
				})
				results <- indexedResult{index: index, result: result, err: err}
			}()
		}
	}

	joined := map[int][]transport.Transport{}
	for range 4 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		joined[result.index] = append(joined[result.index], result.result.Transport)
	}
	defer func() {
		for _, channels := range joined {
			for _, channel := range channels {
				_ = channel.Close()
			}
		}
	}()
	for index, channels := range joined {
		if len(channels) != 2 {
			t.Fatalf("index %d joined channels = %d", index, len(channels))
		}
		payload := []byte{byte('0' + index)}
		if err := channels[0].Send(ctx, payload); err != nil {
			t.Fatal(err)
		}
		got, err := channels[1].Recv(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(payload) {
			t.Fatalf("index %d got %q", index, got)
		}
	}
}

func TestRelayNegotiatesDifferentConnectionCounts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := NewServer().Serve(ctx, ln); err != nil {
			t.Errorf("relay serve failed: %v", err)
		}
	}()

	senderCh := make(chan struct {
		result JoinResult
		err    error
	}, 1)
	go func() {
		result, err := JoinWithOptions(ctx, JoinOptions{
			Addr:            ln.Addr().String(),
			RoomToken:       "count-room",
			Role:            "sender",
			ConnectionCount: 4,
		})
		senderCh <- struct {
			result JoinResult
			err    error
		}{result: result, err: err}
	}()
	receiver, err := JoinWithOptions(ctx, JoinOptions{
		Addr:            ln.Addr().String(),
		RoomToken:       "count-room",
		Role:            "receiver",
		ConnectionCount: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Transport.Close()
	sender := <-senderCh
	if sender.err != nil {
		t.Fatal(sender.err)
	}
	defer sender.result.Transport.Close()
	if sender.result.PeerConnectionCount != 2 {
		t.Fatalf("sender peer count = %d, want 2", sender.result.PeerConnectionCount)
	}
	if receiver.PeerConnectionCount != 4 {
		t.Fatalf("receiver peer count = %d, want 4", receiver.PeerConnectionCount)
	}
}

func TestJoinBundleInheritsPrimaryDialer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addr := startTestRelay(t, ctx)
	var dialCount atomic.Int32
	dialContext := func(ctx context.Context, network, address string) (net.Conn, error) {
		dialCount.Add(1)
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	senderOptions := JoinOptions{
		Addr:            addr,
		RoomToken:       "bundle-dialer-room",
		Role:            "sender",
		ConnectionCount: 2,
		DialContext:     dialContext,
	}
	receiverOptions := senderOptions
	receiverOptions.Role = "receiver"
	type joinOutcome struct {
		result JoinResult
		err    error
	}
	senderPrimaryCh := make(chan joinOutcome, 1)
	go func() {
		result, err := JoinWithOptions(ctx, senderOptions)
		senderPrimaryCh <- joinOutcome{result: result, err: err}
	}()
	receiverPrimary, err := JoinWithOptions(ctx, receiverOptions)
	if err != nil {
		t.Fatal(err)
	}
	senderPrimary := <-senderPrimaryCh
	if senderPrimary.err != nil {
		t.Fatal(senderPrimary.err)
	}

	type bundleOutcome struct {
		transport transport.Transport
		count     int
	}
	senderBundleCh := make(chan bundleOutcome, 1)
	go func() {
		bundle, count := JoinBundle(ctx, senderPrimary.result, addr, senderOptions, 2)
		senderBundleCh <- bundleOutcome{transport: bundle, count: count}
	}()
	receiverBundle, receiverCount := JoinBundle(ctx, receiverPrimary, addr, receiverOptions, 2)
	senderBundle := <-senderBundleCh
	defer receiverBundle.Close()
	defer senderBundle.transport.Close()
	if senderBundle.count != 2 || receiverCount != 2 {
		t.Fatalf("bundle counts sender=%d receiver=%d", senderBundle.count, receiverCount)
	}
	if dialCount.Load() != 4 {
		t.Fatalf("dial count = %d, want 4", dialCount.Load())
	}
}

func TestRelayRejectsInvalidConnectionIndex(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := NewServer().Serve(ctx, ln); err != nil {
			t.Errorf("relay serve failed: %v", err)
		}
	}()
	if _, err := JoinWithOptions(ctx, JoinOptions{
		Addr:            ln.Addr().String(),
		RoomToken:       "invalid-index",
		Role:            "sender",
		ConnectionIndex: 2,
		ConnectionCount: 2,
	}); err == nil {
		t.Fatal("invalid connection index joined")
	}
}

func TestRelayRequiresPasswordWhenConfigured(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer()
	server.pass = "secret"
	go func() {
		if err := server.Serve(ctx, ln); err != nil {
			t.Errorf("relay serve failed: %v", err)
		}
	}()

	if _, err := Join(ctx, ln.Addr().String(), "room-token", "sender", "wrong"); err == nil {
		t.Fatal("join with wrong password succeeded")
	}

	senderCh := make(chan result, 1)
	go func() {
		tp, err := Join(ctx, ln.Addr().String(), "room-token", "sender", "secret")
		senderCh <- result{tp: tp, err: err}
	}()
	time.Sleep(100 * time.Millisecond)
	receiver, err := Join(ctx, ln.Addr().String(), "room-token", "receiver", "secret")
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	sender := (<-senderCh).must(t)
	defer sender.Close()
}

func TestRelayAcceptsTemporaryRoomCredential(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer()
	server.pass = "static-secret"
	server.tokenSecret = "token-secret"
	go func() {
		if err := server.Serve(ctx, ln); err != nil {
			t.Errorf("relay serve failed: %v", err)
		}
	}()

	credential, err := IssueCredential("token-secret", "token-room", time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Join(ctx, ln.Addr().String(), "wrong-room", "sender", credential); err == nil {
		t.Fatal("room-bound credential was accepted for another room")
	}

	senderCh := make(chan result, 1)
	go func() {
		tp, err := Join(ctx, ln.Addr().String(), "token-room", "sender", credential)
		senderCh <- result{tp: tp, err: err}
	}()
	time.Sleep(100 * time.Millisecond)
	receiver, err := Join(ctx, ln.Addr().String(), "token-room", "receiver", credential)
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	sender := (<-senderCh).must(t)
	defer sender.Close()
}

func TestRelayExpiresWaitingRoom(t *testing.T) {
	s := NewServer()
	s.waitTTL = time.Nanosecond
	left, right := net.Pipe()
	defer left.Close()
	go io.Copy(io.Discard, right)
	c := &client{
		t:    transport.NewTCPTransport(left),
		conn: left,
		role: "sender",
		room: "room-token",
	}
	s.waiting["room-token"] = &room{sender: c, created: time.Now().Add(-time.Minute)}
	s.cleanupExpired()
	if _, ok := s.waiting["room-token"]; ok {
		t.Fatal("expired room was not deleted")
	}
	_ = right.Close()
}

type result struct {
	tp interface {
		Send(context.Context, []byte) error
		Recv(context.Context) ([]byte, error)
		Close() error
	}
	err error
}

func (r result) must(t *testing.T) interface {
	Send(context.Context, []byte) error
	Recv(context.Context) ([]byte, error)
	Close() error
} {
	t.Helper()
	if r.err != nil {
		t.Fatal(r.err)
	}
	return r.tp
}
