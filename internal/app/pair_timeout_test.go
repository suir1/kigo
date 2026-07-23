package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/suir1/kigo/internal/transport"
)

func TestPairingWindowReturnsPairTimeout(t *testing.T) {
	window := newPairingWindow(&globalOptions{PairTimeout: 20 * time.Millisecond})
	_, err := withPairingWindow(context.Background(), window, func(ctx context.Context) (struct{}, error) {
		<-ctx.Done()
		return struct{}{}, ctx.Err()
	})
	if !errors.Is(err, errPairTimeout) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
}

func TestPairingWindowPreservesParentCancellation(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	cancel()
	window := newPairingWindow(&globalOptions{PairTimeout: time.Second})
	_, err := withPairingWindow(parent, window, func(context.Context) (struct{}, error) {
		return struct{}{}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
}

func TestPairTimeoutDoesNotLimitConnectedTransfer(t *testing.T) {
	g := &globalOptions{
		Relay:             "relay.test:9000",
		PairTimeout:       20 * time.Millisecond,
		ReconnectAttempts: 1,
	}
	window := newPairingWindow(g)
	err := runTransferWithReconnect(
		context.Background(),
		g,
		window.dialer(func(context.Context) (transport.Transport, error) {
			return &stubTransport{}, nil
		}),
		func(context.Context, transport.Transport) error {
			time.Sleep(50 * time.Millisecond)
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
}

func TestPairTimeoutIsNotRetried(t *testing.T) {
	g := &globalOptions{
		Relay:             "relay.test:9000",
		PairTimeout:       20 * time.Millisecond,
		ReconnectAttempts: 3,
		ReconnectDelay:    time.Millisecond,
	}
	window := newPairingWindow(g)
	dials := 0
	err := runTransferWithReconnect(
		context.Background(),
		g,
		window.dialer(func(ctx context.Context) (transport.Transport, error) {
			dials++
			<-ctx.Done()
			return nil, ctx.Err()
		}),
		func(context.Context, transport.Transport) error { return nil },
	)
	if !errors.Is(err, errPairTimeout) {
		t.Fatalf("error = %v", err)
	}
	if dials != 1 {
		t.Fatalf("dials = %d, want 1", dials)
	}
}

func TestValidatePairTimeout(t *testing.T) {
	if err := validatePairTimeout(defaultPairTimeout); err != nil {
		t.Fatal(err)
	}
	for _, timeout := range []time.Duration{0, -time.Second} {
		if err := validatePairTimeout(timeout); err == nil {
			t.Fatalf("timeout %s was accepted", timeout)
		}
	}
}
