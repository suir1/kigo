package netreuse

import (
	"context"
	"net"
)

func ListenTCP(ctx context.Context, address string) (net.Listener, error) {
	config := net.ListenConfig{Control: socketControl}
	return config.Listen(ctx, "tcp", address)
}

func TCPDialer(localPort int) net.Dialer {
	return TCPDialerForIP(localPort, nil)
}

func TCPDialerForIP(localPort int, localIP net.IP) net.Dialer {
	dialer := net.Dialer{Control: socketControl}
	if localPort > 0 || localIP != nil {
		dialer.LocalAddr = &net.TCPAddr{IP: append(net.IP(nil), localIP...), Port: localPort}
	}
	return dialer
}
