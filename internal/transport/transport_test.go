package transport

import (
	"context"
	"sync"
	"testing"
	"time"
)

type trackedCloser struct {
	once   sync.Once
	closed chan struct{}
}

func (c *trackedCloser) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

func TestCloseOnContextDoneStopsBeforeOwnershipTransfer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	closer := &trackedCloser{closed: make(chan struct{})}
	stop := CloseOnContextDone(ctx, closer)

	stop()
	stop()
	cancel()
	select {
	case <-closer.closed:
		t.Fatal("closer was closed after watcher stopped")
	default:
	}
}

func TestCloseOnContextDoneClosesOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	closer := &trackedCloser{closed: make(chan struct{})}
	stop := CloseOnContextDone(ctx, closer)

	cancel()
	select {
	case <-closer.closed:
	case <-time.After(time.Second):
		t.Fatal("closer was not closed after cancellation")
	}
	stop()
}

func TestAdaptiveSendBudgetUsesQueuePressure(t *testing.T) {
	const (
		maxBudget = 64 * 1024
		minBudget = 16 * 1024
		limit     = 4 * 1024 * 1024
	)
	tests := []struct {
		name    string
		metrics SendMetrics
		want    int
	}{
		{name: "unknown", want: maxBudget},
		{name: "low", metrics: SendMetrics{BufferedBytes: limit / 8, BufferLimit: limit}, want: maxBudget},
		{name: "quarter", metrics: SendMetrics{BufferedBytes: limit / 4, BufferLimit: limit}, want: 32 * 1024},
		{name: "half", metrics: SendMetrics{BufferedBytes: limit / 2, BufferLimit: limit}, want: minBudget},
		{name: "high", metrics: SendMetrics{BufferedBytes: limit * 3 / 4, BufferLimit: limit}, want: minBudget},
		{name: "wait", metrics: SendMetrics{LastWait: 25 * time.Millisecond}, want: minBudget},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AdaptiveSendBudget(maxBudget, minBudget, tt.metrics); got != tt.want {
				t.Fatalf("budget = %d, want %d", got, tt.want)
			}
		})
	}
}
