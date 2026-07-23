package app

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/suir1/kigo/internal/directcandidate"
	"github.com/suir1/kigo/internal/netreuse"
	"github.com/suir1/kigo/internal/transport"
)

const directProtocol = "kigo-direct-v1"
const maxDirectHelloSize = 4 * 1024
const maxDirectCandidates = directcandidate.MaxCandidates

const (
	directLANStartDelay   = 40 * time.Millisecond
	directOtherStartDelay = 100 * time.Millisecond
)

type directHello struct {
	Protocol        string `json:"protocol"`
	RoomToken       string `json:"room_token"`
	Role            string `json:"role"`
	InitiatorRole   string `json:"initiator_role,omitempty"`
	ConnectionIndex int    `json:"connection_index,omitempty"`
	ConnectionCount int    `json:"connection_count,omitempty"`
}

type directConnectResult struct {
	conn                net.Conn
	candidate           string
	peerConnectionCount int
	initiatorRole       string
	err                 error
}

type directDialCandidate struct {
	directcandidate.Candidate
	startDelay time.Duration
}

type directDialContextFunc func(context.Context, string, int) (net.Conn, error)
type directCandidateRunner func(context.Context, directDialCandidate, chan<- directConnectResult)

type directConnectOptions struct {
	candidates  []string
	metadata    []directcandidate.Candidate
	timeout     time.Duration
	roomToken   string
	connections int
	dialContext directDialContextFunc
}

func acceptDirect(ctx context.Context, ln net.Listener, timeout time.Duration, roomToken string) (net.Conn, error) {
	conn, _, err := acceptDirectPrimary(ctx, ln, timeout, roomToken, 1, true)
	return conn, err
}

func acceptDirectBundle(ctx context.Context, ln net.Listener, timeout time.Duration, roomToken string, requested int) (transport.Transport, int, error) {
	if err := validateDirectConnectionCount(requested); err != nil {
		_ = ln.Close()
		return nil, 0, err
	}
	primary, peerCount, err := acceptDirectPrimary(ctx, ln, timeout, roomToken, requested, false)
	if err != nil {
		_ = ln.Close()
		return nil, 0, err
	}
	selected := min(requested, peerCount)
	conns := make([]net.Conn, selected)
	conns[0] = primary
	if selected > 1 {
		if err := acceptDirectAuxiliariesRole(ctx, ln, timeout, roomToken, "sender", selected, conns); err != nil {
			closeDirectConnections(conns)
			_ = ln.Close()
			return nil, 0, err
		}
	}
	_ = ln.Close()
	return directTransportBundle(conns)
}

func acceptDirectPrimary(ctx context.Context, ln net.Listener, timeout time.Duration, roomToken string, requested int, closeListener bool) (net.Conn, int, error) {
	if timeout <= 0 {
		return nil, 0, errors.New("direct timeout must be positive")
	}
	if closeListener {
		defer ln.Close()
	}
	deadline := time.Now().Add(timeout)
	if tcp, ok := ln.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(deadline)
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = ln.Close()
		case <-stop:
		}
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil, 0, ctx.Err()
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				return nil, 0, fmt.Errorf("no verified direct connection within %s", timeout)
			}
			return nil, 0, err
		}
		peerCount, err := verifyDirectSenderIndexed(conn, deadline, roomToken, 0, requested)
		if err != nil {
			_ = conn.Close()
			if time.Now().After(deadline) {
				return nil, 0, fmt.Errorf("no verified direct connection within %s", timeout)
			}
			continue
		}
		clearConnDeadline(conn)
		return conn, peerCount, nil
	}
}

func connectDirect(ctx context.Context, candidates []string, timeout time.Duration, roomToken string) (net.Conn, error) {
	result, err := connectDirectPrimary(ctx, directConnectOptions{
		candidates: candidates, timeout: timeout, roomToken: roomToken, connections: 1,
	})
	return result.conn, err
}

func connectDirectBundle(ctx context.Context, options directConnectOptions) (transport.Transport, int, error) {
	if err := validateDirectConnectionCount(options.connections); err != nil {
		return nil, 0, err
	}
	primary, err := connectDirectPrimary(ctx, options)
	if err != nil {
		return nil, 0, err
	}
	selected := min(options.connections, primary.peerConnectionCount)
	conns := make([]net.Conn, selected)
	conns[0] = primary.conn
	if selected > 1 {
		if err := connectDirectAuxiliariesRoleWithDialer(
			ctx,
			primary.candidate,
			options.timeout,
			options.roomToken,
			"receiver",
			selected,
			conns,
			options.dialContext,
		); err != nil {
			closeDirectConnections(conns)
			return nil, 0, err
		}
	}
	return directTransportBundle(conns)
}

func directTransportBundle(conns []net.Conn) (transport.Transport, int, error) {
	if len(conns) == 0 {
		return nil, 0, errors.New("direct bundle has no connections")
	}
	channels := make([]transport.Transport, len(conns))
	for index, conn := range conns {
		if conn == nil {
			return nil, 0, errors.New("direct bundle has a missing connection")
		}
		channels[index] = transport.NewTCPTransport(conn)
	}
	if len(channels) == 1 {
		return channels[0], 1, nil
	}
	return transport.NewBundle(channels...), len(channels), nil
}

func connectDirectPrimary(ctx context.Context, options directConnectOptions) (directConnectResult, error) {
	ranked := rankDirectCandidates(options.candidates, options.metadata)
	if len(ranked) == 0 {
		return directConnectResult{}, errors.New("peer did not advertise a direct address")
	}
	if options.timeout <= 0 {
		return directConnectResult{}, errors.New("direct timeout must be positive")
	}
	raceCtx, cancel := context.WithTimeout(ctx, options.timeout)
	defer cancel()
	return raceDirectCandidates(raceCtx, ranked, func(
		ctx context.Context,
		candidate directDialCandidate,
		results chan<- directConnectResult,
	) {
		dialDirectCandidate(ctx, candidate, options.roomToken, options.connections, results, options.dialContext)
	})
}

func raceDirectCandidates(
	ctx context.Context,
	candidates []directDialCandidate,
	run directCandidateRunner,
) (directConnectResult, error) {
	raceCtx, cancel := context.WithCancel(ctx)
	results := make(chan directConnectResult, len(candidates))
	for _, candidate := range candidates {
		candidate := candidate
		go run(raceCtx, candidate, results)
	}
	var failures []string
	for completed := range len(candidates) {
		result := <-results
		if result.err == nil {
			cancel()
			clearConnDeadline(result.conn)
			go closeDirectRaceLosers(results, len(candidates)-completed-1)
			return result, nil
		}
		failures = append(failures, result.candidate+": "+result.err.Error())
	}
	cancel()
	return directConnectResult{}, fmt.Errorf("direct candidates failed: %s", strings.Join(failures, "; "))
}

func dialDirectCandidate(
	ctx context.Context,
	candidate directDialCandidate,
	roomToken string,
	requested int,
	results chan<- directConnectResult,
	dialContext directDialContextFunc,
) {
	if candidate.startDelay > 0 {
		timer := time.NewTimer(candidate.startDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			results <- directConnectResult{candidate: candidate.Address, err: ctx.Err()}
			return
		case <-timer.C:
		}
	}
	conn, err := dialDirectTCP(ctx, candidate.Address, 0, dialContext)
	peerCount := 0
	if err == nil {
		peerCount, err = verifyDirectReceiverIndexed(conn, ctx, roomToken, 0, requested)
	}
	if err != nil && conn != nil {
		_ = conn.Close()
		conn = nil
	}
	results <- directConnectResult{
		conn:                conn,
		candidate:           candidate.Address,
		peerConnectionCount: peerCount,
		err:                 err,
	}
}

func rankDirectCandidates(addresses []string, metadata []directcandidate.Candidate) []directDialCandidate {
	candidates := directcandidate.Merge(addresses, metadata)
	if len(candidates) == 0 {
		return nil
	}
	highestPriority := candidates[0].Priority
	out := make([]directDialCandidate, len(candidates))
	for index, candidate := range candidates {
		out[index] = directDialCandidate{
			Candidate:  candidate,
			startDelay: directCandidateStartDelay(candidate.Priority, highestPriority),
		}
	}
	return out
}

func directCandidateStartDelay(priority, highestPriority int) time.Duration {
	if priority == highestPriority || priority >= directcandidate.PriorityIPv6Global {
		return 0
	}
	if priority >= directcandidate.PriorityLAN {
		return directLANStartDelay
	}
	return directOtherStartDelay
}

func closeDirectRaceLosers(results <-chan directConnectResult, remaining int) {
	for range remaining {
		result := <-results
		if result.conn != nil {
			_ = result.conn.Close()
		}
	}
}

func acceptDirectAuxiliariesRole(
	ctx context.Context,
	ln net.Listener,
	timeout time.Duration,
	roomToken string,
	ownRole string,
	count int,
	conns []net.Conn,
) error {
	deadline := time.Now().Add(timeout)
	if tcp, ok := ln.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(deadline)
	}
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = ln.Close()
		case <-stop:
		}
	}()
	remaining := count - 1
	for remaining > 0 {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				return fmt.Errorf("direct auxiliary setup timed out after %s", timeout)
			}
			return err
		}
		index, peerCount, err := verifyDirectAcceptorAny(conn, deadline, roomToken, ownRole, count)
		if err != nil || peerCount != count || index <= 0 || index >= count || conns[index] != nil {
			_ = conn.Close()
			if time.Now().After(deadline) {
				return fmt.Errorf("direct auxiliary setup timed out after %s", timeout)
			}
			continue
		}
		clearConnDeadline(conn)
		conns[index] = conn
		remaining--
	}
	return nil
}

func connectDirectAuxiliariesRoleWithDialer(
	ctx context.Context,
	candidate string,
	timeout time.Duration,
	roomToken string,
	ownRole string,
	count int,
	conns []net.Conn,
	dialContext directDialContextFunc,
) error {
	setupCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	type result struct {
		index int
		conn  net.Conn
		err   error
	}
	results := make(chan result, count-1)
	for index := 1; index < count; index++ {
		index := index
		go func() {
			conn, err := dialDirectTCP(setupCtx, candidate, 0, dialContext)
			if err == nil {
				var peerCount int
				peerCount, err = verifyDirectDialerIndexed(conn, setupCtx, roomToken, ownRole, index, count)
				if err == nil && peerCount != count {
					err = fmt.Errorf("direct peer selected %d connections, want %d", peerCount, count)
				}
			}
			if err != nil && conn != nil {
				_ = conn.Close()
				conn = nil
			}
			results <- result{index: index, conn: conn, err: err}
		}()
	}
	var firstErr error
	for range count - 1 {
		result := <-results
		if result.err != nil {
			if firstErr == nil {
				firstErr = result.err
				cancel()
			}
			continue
		}
		clearConnDeadline(result.conn)
		conns[result.index] = result.conn
	}
	if firstErr != nil {
		return fmt.Errorf("direct auxiliary setup failed: %w", firstErr)
	}
	return nil
}

func dialDirectTCP(ctx context.Context, address string, localPort int, dialContext directDialContextFunc) (net.Conn, error) {
	if dialContext != nil {
		return dialContext(ctx, address, localPort)
	}
	if localPort > 0 {
		dialer := netreuse.TCPDialer(localPort)
		return dialer.DialContext(ctx, "tcp", address)
	}
	var dialer net.Dialer
	return dialer.DialContext(ctx, "tcp", address)
}

func closeDirectConnections(conns []net.Conn) {
	for _, conn := range conns {
		if conn != nil {
			_ = conn.Close()
		}
	}
}

func verifyDirectSender(conn net.Conn, deadline time.Time, roomToken string) error {
	_, err := verifyDirectSenderIndexed(conn, deadline, roomToken, 0, 1)
	return err
}

func verifyDirectSenderIndexed(conn net.Conn, deadline time.Time, roomToken string, expectedIndex, ownCount int) (int, error) {
	return verifyDirectAcceptorIndexed(conn, deadline, roomToken, "sender", expectedIndex, ownCount)
}

func verifyDirectAcceptorIndexed(
	conn net.Conn,
	deadline time.Time,
	roomToken string,
	ownRole string,
	expectedIndex int,
	ownCount int,
) (int, error) {
	peerRole, err := peerDirectRole(ownRole)
	if err != nil {
		return 0, err
	}
	_ = conn.SetDeadline(deadline)
	hello, err := readDirectHello(conn)
	if err != nil {
		return 0, err
	}
	peerCount, ok := directHelloConnectionCount(hello, roomToken, peerRole, expectedIndex)
	if !ok {
		return 0, errors.New("invalid direct peer hello")
	}
	if err := writeDirectHello(conn, newDirectHello(roomToken, ownRole, "", expectedIndex, ownCount)); err != nil {
		return 0, err
	}
	return peerCount, nil
}

func verifyDirectSenderAny(conn net.Conn, deadline time.Time, roomToken string, ownCount int) (int, int, error) {
	return verifyDirectAcceptorAny(conn, deadline, roomToken, "sender", ownCount)
}

func verifyDirectAcceptorAny(
	conn net.Conn,
	deadline time.Time,
	roomToken string,
	ownRole string,
	ownCount int,
) (int, int, error) {
	peerRole, err := peerDirectRole(ownRole)
	if err != nil {
		return 0, 0, err
	}
	_ = conn.SetDeadline(deadline)
	hello, err := readDirectHello(conn)
	if err != nil {
		return 0, 0, err
	}
	peerCount, ok := directHelloConnectionCount(hello, roomToken, peerRole, hello.ConnectionIndex)
	if !ok || hello.ConnectionIndex <= 0 || hello.ConnectionIndex >= ownCount {
		return 0, 0, errors.New("invalid direct peer hello")
	}
	if err := writeDirectHello(conn, newDirectHello(roomToken, ownRole, "", hello.ConnectionIndex, ownCount)); err != nil {
		return 0, 0, err
	}
	return hello.ConnectionIndex, peerCount, nil
}

func verifyDirectReceiver(conn net.Conn, ctx context.Context, roomToken string) error {
	_, err := verifyDirectReceiverIndexed(conn, ctx, roomToken, 0, 1)
	return err
}

func verifyDirectReceiverIndexed(conn net.Conn, ctx context.Context, roomToken string, connectionIndex, ownCount int) (int, error) {
	return verifyDirectDialerIndexed(conn, ctx, roomToken, "receiver", connectionIndex, ownCount)
}

func verifyDirectDialerIndexed(
	conn net.Conn,
	ctx context.Context,
	roomToken string,
	ownRole string,
	connectionIndex int,
	ownCount int,
) (int, error) {
	peerRole, err := peerDirectRole(ownRole)
	if err != nil {
		return 0, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := writeDirectHello(conn, newDirectHello(roomToken, ownRole, "", connectionIndex, ownCount)); err != nil {
		return 0, err
	}
	hello, err := readDirectHello(conn)
	if err != nil {
		return 0, err
	}
	peerCount, ok := directHelloConnectionCount(hello, roomToken, peerRole, connectionIndex)
	if !ok {
		return 0, errors.New("invalid direct peer acknowledgement")
	}
	return peerCount, nil
}

func verifyBidirectionalDirectAcceptorIndexed(
	conn net.Conn,
	deadline time.Time,
	roomToken string,
	ownRole string,
	expectedIndex int,
	ownCount int,
) (int, string, error) {
	peerRole, err := peerDirectRole(ownRole)
	if err != nil {
		return 0, "", err
	}
	_ = conn.SetDeadline(deadline)
	hello, err := readDirectHello(conn)
	if err != nil {
		return 0, "", err
	}
	peerCount, ok := directHelloConnectionCount(hello, roomToken, peerRole, expectedIndex)
	if !ok || hello.InitiatorRole != peerRole {
		return 0, "", errors.New("invalid bidirectional direct peer hello")
	}
	if err := writeDirectHello(conn, newDirectHello(roomToken, ownRole, peerRole, expectedIndex, ownCount)); err != nil {
		return 0, "", err
	}
	return peerCount, peerRole, nil
}

func verifyBidirectionalDirectDialerIndexed(
	conn net.Conn,
	ctx context.Context,
	roomToken string,
	ownRole string,
	connectionIndex int,
	ownCount int,
) (int, string, error) {
	peerRole, err := peerDirectRole(ownRole)
	if err != nil {
		return 0, "", err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := writeDirectHello(conn, newDirectHello(roomToken, ownRole, ownRole, connectionIndex, ownCount)); err != nil {
		return 0, "", err
	}
	hello, err := readDirectHello(conn)
	if err != nil {
		return 0, "", err
	}
	peerCount, ok := directHelloConnectionCount(hello, roomToken, peerRole, connectionIndex)
	if !ok {
		return 0, "", errors.New("invalid bidirectional direct peer acknowledgement")
	}
	switch hello.InitiatorRole {
	case ownRole:
		return peerCount, ownRole, nil
	case peerRole:
		return peerCount, preferredDirectInitiator, nil
	default:
		return 0, "", errors.New("invalid bidirectional direct initiator")
	}
}

func newDirectHello(roomToken, role, initiatorRole string, index, count int) directHello {
	return directHello{
		Protocol:        directProtocol,
		RoomToken:       roomToken,
		Role:            role,
		InitiatorRole:   initiatorRole,
		ConnectionIndex: index,
		ConnectionCount: count,
	}
}

func directHelloConnectionCount(
	hello directHello,
	roomToken string,
	peerRole string,
	expectedIndex int,
) (int, bool) {
	peerCount := normalizedDirectConnectionCount(hello.ConnectionCount)
	valid := hello.Protocol == directProtocol &&
		hello.RoomToken == roomToken &&
		hello.Role == peerRole &&
		hello.ConnectionIndex == expectedIndex &&
		validateDirectConnectionCount(peerCount) == nil
	return peerCount, valid
}

func peerDirectRole(role string) (string, error) {
	switch role {
	case "sender":
		return "receiver", nil
	case "receiver":
		return "sender", nil
	default:
		return "", errors.New("invalid direct role")
	}
}

func normalizedDirectConnectionCount(count int) int {
	if count <= 0 {
		return 1
	}
	return count
}

func validateDirectConnectionCount(count int) error {
	if count < 1 || count > 8 {
		return errors.New("direct connection count must be between 1 and 8")
	}
	return nil
}

func writeDirectHello(w io.Writer, hello directHello) error {
	payload, err := json.Marshal(hello)
	if err != nil {
		return err
	}
	if len(payload) > maxDirectHelloSize {
		return errors.New("direct hello too large")
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if err := writeDirectBytes(w, header[:]); err != nil {
		return err
	}
	return writeDirectBytes(w, payload)
}

func readDirectHello(r io.Reader) (directHello, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return directHello{}, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 || size > maxDirectHelloSize {
		return directHello{}, errors.New("invalid direct hello size")
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return directHello{}, err
	}
	var hello directHello
	if err := json.Unmarshal(payload, &hello); err != nil {
		return directHello{}, err
	}
	return hello, nil
}

func writeDirectBytes(w io.Writer, payload []byte) error {
	for len(payload) > 0 {
		written, err := w.Write(payload)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrUnexpectedEOF
		}
		payload = payload[written:]
	}
	return nil
}

func clearConnDeadline(conn net.Conn) {
	_ = conn.SetDeadline(time.Time{})
}

func uniqueCandidates(candidates []string) []string {
	seen := make(map[string]struct{}, len(candidates))
	out := make([]string, 0, min(len(candidates), maxDirectCandidates))
	for _, candidate := range candidates {
		if len(out) >= maxDirectCandidates {
			break
		}
		if candidate == "" {
			continue
		}
		if _, _, err := net.SplitHostPort(candidate); err != nil {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		out = append(out, candidate)
	}
	return out
}
