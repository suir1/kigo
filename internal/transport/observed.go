package transport

import (
	"context"
	"sync"
	"sync/atomic"
)

type RouteInfo struct {
	Kind        string    `json:"kind"`
	Connections int       `json:"connections"`
	PathWeights []float64 `json:"path_weights,omitempty"`
}

type RouteStats struct {
	RouteInfo
	SentBytes     int64               `json:"sent_bytes"`
	ReceivedBytes int64               `json:"received_bytes"`
	Paths         []PhysicalPathStats `json:"paths,omitempty"`
}

type PhysicalPathStats struct {
	Connection int   `json:"connection"`
	SentBytes  int64 `json:"sent_bytes"`
	SentChunks int64 `json:"sent_chunks"`
	SendNanos  int64 `json:"send_nanos"`
}

type RouteObserver interface {
	RouteStats() RouteStats
}

type PhysicalPathObserver interface {
	RecordPhysicalPathStats([]PhysicalPathStats)
}

type PathWeightProvider interface {
	PhysicalPathWeights() []float64
}

type observedCounters struct {
	sent     atomic.Int64
	received atomic.Int64
}

type countedTransport struct {
	inner    Transport
	counters *observedCounters
}

type ObservedTransport struct {
	countedTransport
	info   RouteInfo
	pathMu sync.RWMutex
	paths  []PhysicalPathStats
}

func Observe(t Transport, info RouteInfo) Transport {
	if t == nil {
		return nil
	}
	if info.Connections <= 0 {
		info.Connections = len(Channels(t))
	}
	return &ObservedTransport{
		countedTransport: countedTransport{inner: t, counters: &observedCounters{}},
		info:             info,
	}
}

func SnapshotRouteStats(t Transport) (RouteStats, bool) {
	observer, ok := t.(RouteObserver)
	if !ok {
		return RouteStats{}, false
	}
	return observer.RouteStats(), true
}

func RecordPhysicalPathStats(t Transport, stats []PhysicalPathStats) {
	if observer, ok := t.(PhysicalPathObserver); ok {
		observer.RecordPhysicalPathStats(stats)
	}
}

func SnapshotPhysicalPathWeights(t Transport) []float64 {
	if provider, ok := t.(PathWeightProvider); ok {
		return provider.PhysicalPathWeights()
	}
	return nil
}

func (t *countedTransport) Send(ctx context.Context, payload []byte) error {
	if err := t.inner.Send(ctx, payload); err != nil {
		return err
	}
	t.counters.sent.Add(int64(len(payload)))
	return nil
}

func (t *countedTransport) Recv(ctx context.Context) ([]byte, error) {
	payload, err := t.inner.Recv(ctx)
	if err != nil {
		return nil, err
	}
	t.counters.received.Add(int64(len(payload)))
	return payload, nil
}

func (t *countedTransport) Close() error {
	return t.inner.Close()
}

func (t *ObservedTransport) Channels() []Transport {
	channels := Channels(t.inner)
	observed := make([]Transport, len(channels))
	for index, channel := range channels {
		observed[index] = &countedTransport{
			inner:    channel,
			counters: t.counters,
		}
	}
	return observed
}

func (t *ObservedTransport) RouteStats() RouteStats {
	t.pathMu.RLock()
	paths := append([]PhysicalPathStats(nil), t.paths...)
	t.pathMu.RUnlock()
	return RouteStats{
		RouteInfo:     t.info,
		SentBytes:     t.counters.sent.Load(),
		ReceivedBytes: t.counters.received.Load(),
		Paths:         paths,
	}
}

func (t *ObservedTransport) RecordPhysicalPathStats(stats []PhysicalPathStats) {
	t.pathMu.Lock()
	t.paths = append([]PhysicalPathStats(nil), stats...)
	t.pathMu.Unlock()
}

func (t *ObservedTransport) PhysicalPathWeights() []float64 {
	return append([]float64(nil), t.info.PathWeights...)
}
