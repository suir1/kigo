package transport

import (
	"testing"
	"time"
)

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
