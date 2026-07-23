package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/suir1/kigo/internal/transport"
)

const defaultPairTimeout = 5 * time.Minute

var errPairTimeout = errors.New("pairing timed out")

type pairTimeoutError struct {
	timeout time.Duration
}

func (e pairTimeoutError) Error() string {
	return fmt.Sprintf("pairing timed out after %s waiting for peer", e.timeout)
}

func (e pairTimeoutError) Is(target error) bool {
	return target == errPairTimeout
}

type pairingWindow struct {
	timeout  time.Duration
	deadline time.Time
}

func newPairingWindow(g *globalOptions) *pairingWindow {
	timeout := normalizedPairTimeout(g)
	return &pairingWindow{
		timeout:  timeout,
		deadline: time.Now().Add(timeout),
	}
}

func normalizedPairTimeout(g *globalOptions) time.Duration {
	if g == nil || g.PairTimeout <= 0 {
		return defaultPairTimeout
	}
	return g.PairTimeout
}

func validatePairTimeout(timeout time.Duration) error {
	if timeout <= 0 {
		return errors.New("pair timeout must be positive")
	}
	return nil
}

func withPairingWindow[T any](
	parent context.Context,
	window *pairingWindow,
	operation func(context.Context) (T, error),
) (T, error) {
	return withPairingDeadline(parent, window.deadline, window.timeout, operation)
}

func (w *pairingWindow) dialer(dial transportDialer) transportDialer {
	first := true
	return func(parent context.Context) (transport.Transport, error) {
		deadline := time.Now().Add(w.timeout)
		if first {
			first = false
			deadline = w.deadline
		}
		return withPairingDeadline(parent, deadline, w.timeout, dial)
	}
}

func withPairingDeadline[T any](
	parent context.Context,
	deadline time.Time,
	timeout time.Duration,
	operation func(context.Context) (T, error),
) (T, error) {
	var zero T
	if err := parent.Err(); err != nil {
		return zero, err
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	result, err := operation(ctx)
	cancel()
	if err == nil {
		return result, nil
	}
	if parentErr := parent.Err(); parentErr != nil {
		return zero, parentErr
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || !time.Now().Before(deadline) {
		return zero, pairTimeoutError{timeout: timeout}
	}
	return zero, err
}
