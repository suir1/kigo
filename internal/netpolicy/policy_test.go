package netpolicy

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestResolveEmptyAndUnknownInterface(t *testing.T) {
	policy, err := Resolve("")
	if err != nil || policy != nil {
		t.Fatalf("empty policy = %#v, %v", policy, err)
	}
	if _, err := Resolve("kigo-interface-that-does-not-exist"); err == nil {
		t.Fatal("unknown interface was accepted")
	}
}

func TestPolicyBindsTCPDialToSelectedLoopbackInterface(t *testing.T) {
	interfaceName := interfaceForIP(t, net.ParseIP("127.0.0.1"))
	policy, err := Resolve(interfaceName)
	if err != nil {
		t.Fatal(err)
	}
	if !policy.InterfaceFilter(interfaceName) || policy.InterfaceFilter(interfaceName+"-other") {
		t.Fatal("interface filter did not select exactly one interface")
	}
	if !policy.IPFilter(net.ParseIP("127.0.0.1")) {
		t.Fatalf("policy addresses = %v", policy.IPs())
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		accepted <- conn
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := policy.DialContext(ctx, "tcp4", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	local := conn.LocalAddr().(*net.TCPAddr).IP
	if !policy.ContainsIP(local) {
		t.Fatalf("dial local IP = %s, policy addresses = %v", local, policy.IPs())
	}
	if peer := <-accepted; peer != nil {
		_ = peer.Close()
	}
}

func TestTCPAddrForMatchesTargetFamily(t *testing.T) {
	interfaceName := interfaceForIP(t, net.ParseIP("127.0.0.1"))
	policy, err := Resolve(interfaceName)
	if err != nil {
		t.Fatal(err)
	}
	network, address, err := policy.TCPAddrFor("127.0.0.1:9000", 12345)
	if err != nil {
		t.Fatal(err)
	}
	if network != "tcp4" || address.Port != 12345 || !address.IP.Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("TCP source = %s %#v", network, address)
	}
}

func interfaceForIP(t *testing.T, target net.IP) string {
	t.Helper()
	interfaces, err := net.Interfaces()
	if err != nil {
		t.Fatal(err)
	}
	for _, iface := range interfaces {
		addresses, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, address := range addresses {
			if ip := addressIP(address); ip != nil && ip.Equal(target) {
				return iface.Name
			}
		}
	}
	t.Fatalf("no interface owns %s", target)
	return ""
}
