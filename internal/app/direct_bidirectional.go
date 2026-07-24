package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/suir1/kigo/internal/directcandidate"
	"github.com/suir1/kigo/internal/transport"
)

const preferredDirectInitiator = "receiver"

type bidirectionalPrimaryResult struct {
	directConnectResult
}

func connectBidirectionalDirectBundle(
	ctx context.Context,
	listener net.Listener,
	ownRole string,
	punchAt time.Time,
	options directConnectOptions,
) (transport.Transport, int, error) {
	if listener == nil {
		return nil, 0, errors.New("bidirectional direct listener is required")
	}
	if _, err := peerDirectRole(ownRole); err != nil {
		_ = listener.Close()
		return nil, 0, err
	}
	if err := validateDirectConnectionCount(options.connections); err != nil {
		_ = listener.Close()
		return nil, 0, err
	}
	primary, err := connectBidirectionalDirectPrimary(ctx, listener, ownRole, punchAt, options)
	if err != nil {
		_ = listener.Close()
		return nil, 0, err
	}
	selected := min(options.connections, primary.peerConnectionCount)
	conns := make([]net.Conn, selected)
	conns[0] = primary.conn
	if selected > 1 {
		if ownRole == "receiver" {
			auxCandidate := primary.candidate
			if auxCandidate == "" {
				ranked := rankDirectCandidates(options.candidates, options.metadata)
				if len(ranked) > 0 {
					auxCandidate = ranked[0].Address
				}
			}
			if auxCandidate == "" {
				closeDirectConnections(conns)
				_ = listener.Close()
				return nil, 0, errors.New("peer direct connection has no auxiliary candidate")
			}
			err = connectDirectAuxiliariesRoleWithDialer(
				ctx,
				auxCandidate,
				options.timeout,
				options.roomToken,
				ownRole,
				selected,
				conns,
				options.dialContext,
			)
		} else {
			err = acceptDirectAuxiliariesRole(
				ctx,
				listener,
				options.timeout,
				options.roomToken,
				ownRole,
				selected,
				conns,
			)
		}
		if err != nil {
			closeDirectConnections(conns)
			_ = listener.Close()
			return nil, 0, err
		}
	}
	_ = listener.Close()
	return directTransportBundle(conns)
}

func connectBidirectionalDirectPrimary(
	ctx context.Context,
	listener net.Listener,
	ownRole string,
	punchAt time.Time,
	options directConnectOptions,
) (bidirectionalPrimaryResult, error) {
	if options.timeout <= 0 {
		return bidirectionalPrimaryResult{}, errors.New("direct timeout must be positive")
	}
	if _, err := peerDirectRole(ownRole); err != nil {
		return bidirectionalPrimaryResult{}, err
	}
	punchStart := normalizedDirectPunchStart(punchAt)
	phaseCtx, cancel := context.WithDeadline(ctx, punchStart.Add(options.timeout))
	outcomes := make(chan bidirectionalPrimaryResult, 2)
	go func() {
		result := acceptBidirectionalDirectPrimary(
			phaseCtx,
			listener,
			options.roomToken,
			ownRole,
			options.connections,
		)
		outcomes <- result
	}()
	go func() {
		dialAt := punchStart
		if ownRole == "sender" {
			dialAt = dialAt.Add(preferredDirectPhaseBudget(options.timeout))
		}
		if err := waitForDirectPunch(phaseCtx, dialAt); err != nil {
			outcomes <- bidirectionalPrimaryResult{
				directConnectResult: directConnectResult{err: err},
			}
			return
		}
		result := dialBidirectionalDirectPrimary(
			phaseCtx,
			options.candidates,
			options.metadata,
			directListenerPort(listener),
			options.roomToken,
			ownRole,
			options.connections,
			options.dialContext,
		)
		outcomes <- result
	}()

	var fallback *bidirectionalPrimaryResult
	var failures []string
	remaining := 2
	for remaining > 0 {
		select {
		case outcome := <-outcomes:
			remaining--
			if outcome.err != nil {
				failures = append(failures, outcome.err.Error())
				if fallback != nil && remaining == 0 {
					return finishBidirectionalPrimary(listener, cancel, outcomes, remaining, *fallback), nil
				}
				continue
			}
			if outcome.initiatorRole == preferredDirectInitiator {
				if fallback != nil && fallback.conn != nil {
					_ = fallback.conn.Close()
				}
				return finishBidirectionalPrimary(listener, cancel, outcomes, remaining, outcome), nil
			}
			selected := outcome
			fallback = &selected
			if remaining == 0 {
				return finishBidirectionalPrimary(listener, cancel, outcomes, remaining, selected), nil
			}
		case <-phaseCtx.Done():
			if fallback != nil {
				return finishBidirectionalPrimary(listener, cancel, outcomes, remaining, *fallback), nil
			}
			finishBidirectionalPrimary(
				listener,
				cancel,
				outcomes,
				remaining,
				bidirectionalPrimaryResult{},
			)
			if len(failures) == 0 {
				return bidirectionalPrimaryResult{}, phaseCtx.Err()
			}
			return bidirectionalPrimaryResult{}, fmt.Errorf(
				"bidirectional direct failed: %s",
				strings.Join(failures, "; "),
			)
		}
	}
	cancel()
	if fallback != nil {
		clearConnDeadline(fallback.conn)
		return *fallback, nil
	}
	if len(failures) == 0 {
		return bidirectionalPrimaryResult{}, errors.New("bidirectional direct ended without a result")
	}
	return bidirectionalPrimaryResult{}, fmt.Errorf(
		"bidirectional direct failed: %s",
		strings.Join(failures, "; "),
	)
}

func finishBidirectionalPrimary(
	listener net.Listener,
	cancel context.CancelFunc,
	outcomes <-chan bidirectionalPrimaryResult,
	remaining int,
	selected bidirectionalPrimaryResult,
) bidirectionalPrimaryResult {
	cancel()
	if tcp, ok := listener.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Now())
	}
	for range remaining {
		outcome := <-outcomes
		if outcome.conn != nil && outcome.conn != selected.conn {
			_ = outcome.conn.Close()
		}
	}
	if tcp, ok := listener.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(time.Time{})
	}
	if selected.conn != nil {
		clearConnDeadline(selected.conn)
	}
	return selected
}

func acceptBidirectionalDirectPrimary(
	ctx context.Context,
	listener net.Listener,
	roomToken string,
	ownRole string,
	requested int,
) bidirectionalPrimaryResult {
	deadline, ok := ctx.Deadline()
	if !ok {
		return bidirectionalPrimaryResult{
			directConnectResult: directConnectResult{err: errors.New("direct accept deadline is required")},
		}
	}
	if tcp, ok := listener.(*net.TCPListener); ok {
		_ = tcp.SetDeadline(deadline)
	}
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			return bidirectionalPrimaryResult{
				directConnectResult: directConnectResult{err: err},
			}
		}
		stopContextWatch := transport.CloseOnContextDone(ctx, conn)
		peerCount, initiatorRole, verifyErr := verifyBidirectionalDirectAcceptorIndexed(
			conn,
			deadline,
			roomToken,
			ownRole,
			0,
			requested,
		)
		stopContextWatch()
		if verifyErr != nil {
			_ = conn.Close()
			if ctx.Err() != nil {
				return bidirectionalPrimaryResult{
					directConnectResult: directConnectResult{err: ctx.Err()},
				}
			}
			continue
		}
		clearConnDeadline(conn)
		return bidirectionalPrimaryResult{
			directConnectResult: directConnectResult{
				conn:                conn,
				peerConnectionCount: peerCount,
				initiatorRole:       initiatorRole,
			},
		}
	}
}

func dialBidirectionalDirectPrimary(
	ctx context.Context,
	addresses []string,
	metadata []directcandidate.Candidate,
	localPort int,
	roomToken string,
	ownRole string,
	requested int,
	dialContext directDialContextFunc,
) bidirectionalPrimaryResult {
	ranked := rankDirectCandidates(addresses, metadata)
	if len(ranked) == 0 {
		return bidirectionalPrimaryResult{
			directConnectResult: directConnectResult{
				err: errors.New("peer did not advertise a direct address"),
			},
		}
	}
	result, err := raceDirectCandidates(ctx, ranked, func(
		ctx context.Context,
		candidate directDialCandidate,
		results chan<- directConnectResult,
	) {
		dialBidirectionalCandidate(
			ctx, candidate, localPort, roomToken, ownRole, requested, results, dialContext,
		)
	})
	if err == nil {
		return bidirectionalPrimaryResult{directConnectResult: result}
	}
	return bidirectionalPrimaryResult{
		directConnectResult: directConnectResult{
			err: err,
		},
	}
}

func dialBidirectionalCandidate(
	ctx context.Context,
	candidate directDialCandidate,
	localPort int,
	roomToken string,
	ownRole string,
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
	conn, err := dialDirectTCP(ctx, candidate.Address, localPort, dialContext)
	peerCount := 0
	if err == nil {
		var initiatorRole string
		peerCount, initiatorRole, err = verifyBidirectionalDirectDialerIndexed(
			conn,
			ctx,
			roomToken,
			ownRole,
			0,
			requested,
		)
		if err == nil {
			results <- directConnectResult{
				conn:                conn,
				candidate:           candidate.Address,
				peerConnectionCount: peerCount,
				initiatorRole:       initiatorRole,
			}
			return
		}
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

func waitForDirectPunch(ctx context.Context, punchAt time.Time) error {
	if punchAt.IsZero() || !punchAt.After(time.Now()) {
		return nil
	}
	if punchAt.After(time.Now().Add(2 * time.Second)) {
		return nil
	}
	timer := time.NewTimer(time.Until(punchAt))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func normalizedDirectPunchStart(punchAt time.Time) time.Time {
	now := time.Now()
	if punchAt.IsZero() || punchAt.Before(now) || punchAt.After(now.Add(2*time.Second)) {
		return now
	}
	return punchAt
}

func preferredDirectPhaseBudget(timeout time.Duration) time.Duration {
	budget := timeout * 2 / 3
	if budget < 250*time.Millisecond {
		budget = 250 * time.Millisecond
	}
	if budget > timeout {
		return timeout
	}
	return budget
}
