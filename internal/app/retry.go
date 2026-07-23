package app

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/suir1/kigo/internal/transport"
)

type transportDialer func(context.Context) (transport.Transport, error)
type transferAttempt func(context.Context, transport.Transport) error
type reconnectGate func() bool

func runTransferWithReconnect(ctx context.Context, g *globalOptions, dial transportDialer, attempt transferAttempt) error {
	return runTransferWithReconnectGate(ctx, g, nil, dial, attempt)
}

func runTransferWithReconnectGate(
	ctx context.Context,
	g *globalOptions,
	gate reconnectGate,
	dial transportDialer,
	attempt transferAttempt,
) error {
	maxAttempts, err := totalReconnectAttempts(g, gate != nil)
	if err != nil {
		return err
	}
	for current := 1; current <= maxAttempts; current++ {
		t, attemptErr := dial(ctx)
		if attemptErr == nil {
			started := time.Now()
			attemptErr = attempt(ctx, t)
			_ = t.Close()
			recordObservedRoute(g, t, attemptErr == nil, time.Since(started))
		}
		if attemptErr == nil {
			return nil
		}
		if current >= maxAttempts ||
			!isRetryableTransferError(attemptErr) ||
			(gate != nil && !gate()) {
			return attemptErr
		}
		taskLogf(g, "connection interrupted: %v", attemptErr)
		taskLogf(g, "reconnecting attempt %d/%d in %s", current+1, maxAttempts, g.ReconnectDelay)
		timer := time.NewTimer(g.ReconnectDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func totalReconnectAttempts(g *globalOptions, webRTCReconnectConfigured ...bool) (int, error) {
	if g.NoReconnect ||
		(planNativeRoute(g).Kind == routeWebRTC &&
			(len(webRTCReconnectConfigured) == 0 || !webRTCReconnectConfigured[0])) {
		return 1, nil
	}
	if g.ReconnectAttempts < 1 {
		return 0, errors.New("reconnect attempts must be at least 1")
	}
	if g.ReconnectDelay < 0 {
		return 0, errors.New("reconnect delay cannot be negative")
	}
	return g.ReconnectAttempts, nil
}

func isRetryableTransferError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	if errors.Is(err, errPairTimeout) {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, transport.ErrClosed) ||
		errors.Is(err, net.ErrClosed) {
		return true
	}
	var networkError net.Error
	if errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary()) {
		return true
	}
	for _, target := range []error{
		syscall.ECONNRESET,
		syscall.ECONNREFUSED,
		syscall.ECONNABORTED,
		syscall.EPIPE,
		syscall.ETIMEDOUT,
		syscall.ENETDOWN,
		syscall.ENETUNREACH,
		syscall.EHOSTUNREACH,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	message := strings.ToLower(err.Error())
	for _, phrase := range []string{
		"all relay candidates failed",
		"broken pipe",
		"connection closed",
		"connection lost",
		"connection reset",
		"data channel closed",
		"failed to connect",
		"network is unreachable",
		"use of closed network connection",
	} {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}
