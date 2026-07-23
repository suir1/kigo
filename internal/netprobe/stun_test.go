package netprobe

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/pion/stun/v3"
)

type testSTUNServer struct {
	conn  *net.UDPConn
	done  chan struct{}
	mapFn func(*net.UDPAddr) *net.UDPAddr
	other *net.UDPAddr
}

func newTestSTUNServer(
	t *testing.T,
	mapFn func(*net.UDPAddr) *net.UDPAddr,
	other *net.UDPAddr,
) *testSTUNServer {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	server := &testSTUNServer{
		conn:  conn,
		done:  make(chan struct{}),
		mapFn: mapFn,
		other: other,
	}
	go server.serve()
	t.Cleanup(func() {
		_ = conn.Close()
		<-server.done
	})
	return server
}

func (s *testSTUNServer) URL() string {
	return "stun:" + s.conn.LocalAddr().String()
}

func (s *testSTUNServer) serve() {
	defer close(s.done)
	buffer := make([]byte, 1500)
	for {
		n, remote, err := s.conn.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		request := &stun.Message{Raw: append([]byte(nil), buffer[:n]...)}
		if err := request.Decode(); err != nil {
			continue
		}
		mapped := s.mapFn(remote)
		setters := []stun.Setter{
			stun.NewTransactionIDSetter(request.TransactionID),
			stun.BindingSuccess,
			&stun.XORMappedAddress{IP: mapped.IP, Port: mapped.Port},
		}
		if s.other != nil {
			setters = append(setters, &stun.OtherAddress{
				IP:   s.other.IP,
				Port: s.other.Port,
			})
		}
		response, err := stun.Build(setters...)
		if err != nil {
			continue
		}
		_, _ = s.conn.WriteToUDP(response.Raw, remote)
	}
}

func fixedMapping(port int) func(*net.UDPAddr) *net.UDPAddr {
	return func(*net.UDPAddr) *net.UDPAddr {
		return &net.UDPAddr{IP: net.ParseIP("203.0.113.10"), Port: port}
	}
}

func TestProbeSTUNClassifiesOpenMapping(t *testing.T) {
	first := newTestSTUNServer(t, func(remote *net.UDPAddr) *net.UDPAddr {
		return remote
	}, nil)
	second := newTestSTUNServer(t, func(remote *net.UDPAddr) *net.UDPAddr {
		return remote
	}, nil)
	report := ProbeSTUN(
		context.Background(),
		[]string{first.URL(), second.URL()},
		time.Second,
	)
	if !report.OK || report.Class != NATOpen || len(report.Observations) != 2 {
		t.Fatalf("report = %#v", report)
	}
}

func TestProbeSTUNBindsConfiguredSourceAddress(t *testing.T) {
	server := newTestSTUNServer(t, func(remote *net.UDPAddr) *net.UDPAddr {
		return remote
	}, nil)
	loopback := net.ParseIP("127.0.0.1")
	report := ProbeSTUNWithOptions(
		context.Background(),
		[]string{server.URL()},
		time.Second,
		STUNOptions{IPv4: loopback, InterfaceIPs: []net.IP{loopback}},
	)
	if !report.OK || !strings.HasPrefix(report.Local, "127.0.0.1:") {
		t.Fatalf("bound STUN report = %#v", report)
	}
}

func TestProbeSTUNClassifiesConeMapping(t *testing.T) {
	first := newTestSTUNServer(t, fixedMapping(45000), nil)
	second := newTestSTUNServer(t, fixedMapping(45000), nil)
	report := ProbeSTUN(
		context.Background(),
		[]string{first.URL(), second.URL()},
		time.Second,
	)
	if !report.OK || report.Class != NATCone || len(report.Observations) != 2 {
		t.Fatalf("report = %#v", report)
	}
}

func TestProbeSTUNClassifiesSymmetricMapping(t *testing.T) {
	first := newTestSTUNServer(t, fixedMapping(45000), nil)
	second := newTestSTUNServer(t, fixedMapping(45001), nil)
	report := ProbeSTUN(
		context.Background(),
		[]string{first.URL(), second.URL()},
		time.Second,
	)
	if !report.OK || report.Class != NATSymmetric {
		t.Fatalf("report = %#v", report)
	}
}

func TestProbeSTUNUsesOtherAddress(t *testing.T) {
	second := newTestSTUNServer(t, fixedMapping(45000), nil)
	first := newTestSTUNServer(
		t,
		fixedMapping(45000),
		second.conn.LocalAddr().(*net.UDPAddr),
	)
	report := ProbeSTUN(context.Background(), []string{first.URL()}, time.Second)
	if !report.OK || report.Class != NATCone || len(report.Observations) != 2 {
		t.Fatalf("report = %#v", report)
	}
	if report.Observations[1].Server != "stun:"+second.conn.LocalAddr().String() {
		t.Fatalf("second server = %q", report.Observations[1].Server)
	}
}

func TestProbeSTUNNeedsTwoMappingsToClassifyNAT(t *testing.T) {
	server := newTestSTUNServer(t, fixedMapping(45000), nil)
	report := ProbeSTUN(context.Background(), []string{server.URL()}, time.Second)
	if !report.OK || report.Class != NATUnknown || len(report.Observations) != 1 {
		t.Fatalf("report = %#v", report)
	}
}

func TestParseSTUNEndpointsKeepsOnlyUDPSTUN(t *testing.T) {
	got := parseSTUNEndpoints([]string{
		"stun:stun.example:3478",
		"stun:stun.example:3478",
		"stuns:secure.example:5349",
		"turn:turn.example:3478",
		"bad",
	})
	if len(got) != 1 || got[0].host != "stun.example" || got[0].port != 3478 {
		t.Fatalf("endpoints = %#v", got)
	}
}

func TestValidNATClass(t *testing.T) {
	for _, value := range []string{"unknown", "open", "cone", "symmetric"} {
		if !ValidNATClass(value) {
			t.Fatalf("%q should be valid", value)
		}
	}
	for _, value := range []string{"", "nat", "symmetric-nat"} {
		if ValidNATClass(value) {
			t.Fatalf("%q should be invalid", value)
		}
	}
}
