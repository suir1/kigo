package netprobe

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"net"
	"slices"
	"strconv"
	"sync"
	"time"
)

const (
	udpPunchPacketCount = 32
	udpPunchPacketSize  = 33
	udpPunchMACSize     = 16
	udpPunchMaxWait     = 2 * time.Second
	udpPunchWindow      = 400 * time.Millisecond
)

var udpPunchMagic = [8]byte{'K', 'I', 'G', 'O', 'U', 'D', 'P', '1'}

type UDPPunchResult struct {
	Received bool
	Peer     string
}

// UDPPuncher owns the socket used by the preceding STUN probe. It is safe to
// close while Punch is running; UDP failure is always advisory to TCP direct.
type UDPPuncher struct {
	conn       *net.UDPConn
	candidates []string
	closeOnce  sync.Once
}

func newUDPPuncher(conn *net.UDPConn, report STUNReport, opts STUNOptions) *UDPPuncher {
	return &UDPPuncher{
		conn:       conn,
		candidates: udpPunchCandidates(conn, report, opts),
	}
}

func (p *UDPPuncher) Candidates() []string {
	if p == nil {
		return nil
	}
	return append([]string(nil), p.candidates...)
}

func (p *UDPPuncher) Close() error {
	if p == nil || p.conn == nil {
		return nil
	}
	var err error
	p.closeOnce.Do(func() {
		err = p.conn.Close()
	})
	return err
}

func (p *UDPPuncher) Punch(
	ctx context.Context,
	peerCandidates []string,
	roomToken string,
	role string,
	punchAt time.Time,
) (UDPPunchResult, error) {
	if p == nil || p.conn == nil {
		return UDPPunchResult{}, errors.New("UDP punch socket is unavailable")
	}
	peerRole, err := oppositePunchRole(role)
	if err != nil {
		return UDPPunchResult{}, err
	}
	if roomToken == "" {
		return UDPPunchResult{}, errors.New("UDP punch room token is required")
	}
	targets := resolveUDPPunchTargets(peerCandidates)
	if len(targets) == 0 {
		return UDPPunchResult{}, errors.New("peer has no usable UDP punch candidate")
	}
	start := normalizedUDPPunchStart(punchAt)
	punchAtMillis := punchAt.UnixMilli()
	if punchAt.IsZero() {
		punchAtMillis = start.UnixMilli()
	}
	deadline := start.Add(udpPunchWindow)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if !deadline.After(time.Now()) {
		return UDPPunchResult{}, context.DeadlineExceeded
	}
	packet := makeUDPPunchPacket(roomToken, role, punchAtMillis)
	sendCtx, cancelSend := context.WithCancel(ctx)
	defer cancelSend()
	go sendUDPPunchBurst(sendCtx, p.conn, targets, packet, start, deadline)

	if err := p.conn.SetReadDeadline(deadline); err != nil {
		return UDPPunchResult{}, err
	}
	buffer := make([]byte, 256)
	for {
		n, peer, readErr := p.conn.ReadFromUDP(buffer)
		if readErr != nil {
			if ctx.Err() != nil {
				return UDPPunchResult{}, ctx.Err()
			}
			var netErr net.Error
			if errors.As(readErr, &netErr) && netErr.Timeout() {
				return UDPPunchResult{}, nil
			}
			if errors.Is(readErr, net.ErrClosed) {
				return UDPPunchResult{}, nil
			}
			return UDPPunchResult{}, readErr
		}
		if validUDPPunchPacket(buffer[:n], roomToken, peerRole, punchAtMillis) {
			_, _ = p.conn.WriteToUDP(packet, peer)
			return UDPPunchResult{Received: true, Peer: peer.String()}, nil
		}
	}
}

func udpPunchCandidates(conn *net.UDPConn, report STUNReport, opts STUNOptions) []string {
	if conn == nil {
		return nil
	}
	local, _ := conn.LocalAddr().(*net.UDPAddr)
	if local == nil || local.Port < 1 {
		return nil
	}
	var ips []net.IP
	if local.IP != nil && !local.IP.IsUnspecified() {
		ips = append(ips, local.IP)
	} else if len(opts.InterfaceIPs) > 0 {
		ips = append(ips, opts.InterfaceIPs...)
	} else {
		ips = localInterfaceIPs()
	}
	var candidates []string
	add := func(address string) {
		if len(candidates) >= 8 || address == "" || slices.Contains(candidates, address) {
			return
		}
		candidates = append(candidates, address)
	}
	for _, observation := range report.Observations {
		if address, err := net.ResolveUDPAddr("udp", observation.Mapped); err == nil &&
			address.IP != nil && address.Port > 0 {
			add(address.String())
		}
	}
	for _, ip := range ips {
		if !usableUDPPunchIP(ip, local.IP) {
			continue
		}
		add(net.JoinHostPort(ip.String(), strconv.Itoa(local.Port)))
	}
	return candidates
}

func usableUDPPunchIP(ip, bound net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() {
		return false
	}
	if bound != nil && bound.To4() != nil {
		return ip.To4() != nil
	}
	if bound != nil && bound.To4() == nil && bound.To16() != nil {
		return ip.To4() == nil && ip.To16() != nil
	}
	return true
}

func resolveUDPPunchTargets(candidates []string) []*net.UDPAddr {
	out := make([]*net.UDPAddr, 0, min(len(candidates), 8))
	seen := map[string]struct{}{}
	for _, candidate := range candidates {
		if len(out) >= 8 {
			break
		}
		address, err := net.ResolveUDPAddr("udp", candidate)
		if err != nil || address.IP == nil || address.Port < 1 {
			continue
		}
		key := address.String()
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, address)
	}
	return out
}

func sendUDPPunchBurst(
	ctx context.Context,
	conn *net.UDPConn,
	targets []*net.UDPAddr,
	packet []byte,
	start time.Time,
	deadline time.Time,
) {
	if err := waitForUDPPunch(ctx, start); err != nil {
		return
	}
	interval := max(time.Until(deadline)/udpPunchPacketCount, time.Millisecond)
	for index := 0; index < udpPunchPacketCount; index++ {
		if ctx.Err() != nil || !time.Now().Before(deadline) {
			return
		}
		_, _ = conn.WriteToUDP(packet, targets[index%len(targets)])
		if index+1 == udpPunchPacketCount {
			return
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func makeUDPPunchPacket(roomToken, role string, punchAtMillis int64) []byte {
	packet := make([]byte, udpPunchPacketSize)
	copy(packet[:8], udpPunchMagic[:])
	packet[8] = punchRoleByte(role)
	binary.BigEndian.PutUint64(packet[9:17], uint64(punchAtMillis))
	mac := hmac.New(sha256.New, []byte(roomToken))
	_, _ = mac.Write(packet[:17])
	copy(packet[17:], mac.Sum(nil)[:udpPunchMACSize])
	return packet
}

func validUDPPunchPacket(packet []byte, roomToken, role string, punchAtMillis int64) bool {
	if len(packet) != udpPunchPacketSize ||
		subtle.ConstantTimeCompare(packet[:8], udpPunchMagic[:]) != 1 ||
		packet[8] != punchRoleByte(role) ||
		int64(binary.BigEndian.Uint64(packet[9:17])) != punchAtMillis {
		return false
	}
	want := makeUDPPunchPacket(roomToken, role, punchAtMillis)
	return hmac.Equal(packet[17:], want[17:])
}

func punchRoleByte(role string) byte {
	switch role {
	case "sender":
		return 1
	case "receiver":
		return 2
	default:
		return 0
	}
}

func oppositePunchRole(role string) (string, error) {
	switch role {
	case "sender":
		return "receiver", nil
	case "receiver":
		return "sender", nil
	default:
		return "", errors.New("invalid UDP punch role")
	}
}

func normalizedUDPPunchStart(punchAt time.Time) time.Time {
	now := time.Now()
	if punchAt.IsZero() || punchAt.Before(now) || punchAt.After(now.Add(udpPunchMaxWait)) {
		return now
	}
	return punchAt
}

func waitForUDPPunch(ctx context.Context, start time.Time) error {
	if !start.After(time.Now()) {
		return nil
	}
	timer := time.NewTimer(time.Until(start))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
