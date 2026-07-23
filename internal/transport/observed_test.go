package transport

import (
	"context"
	"errors"
	"testing"
	"time"
)

type observedStub struct {
	recv []byte
}

func (s *observedStub) Send(context.Context, []byte) error {
	return nil
}

func (s *observedStub) Recv(context.Context) ([]byte, error) {
	if s.recv == nil {
		return nil, errors.New("empty")
	}
	payload := s.recv
	s.recv = nil
	return payload, nil
}

func (s *observedStub) Close() error {
	return nil
}

func TestObservedTransportAggregatesBundleBytes(t *testing.T) {
	first := &observedStub{recv: []byte("abc")}
	second := &observedStub{recv: []byte("de")}
	observed := Observe(NewBundle(first, second), RouteInfo{Kind: "direct", Connections: 2})
	channels := Channels(observed)
	if err := channels[0].Send(context.Background(), []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, err := channels[0].Recv(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := channels[1].Send(context.Background(), []byte("world!")); err != nil {
		t.Fatal(err)
	}
	if _, err := channels[1].Recv(context.Background()); err != nil {
		t.Fatal(err)
	}
	stats, ok := SnapshotRouteStats(observed)
	if !ok {
		t.Fatal("route stats unavailable")
	}
	if stats.Kind != "direct" || stats.Connections != 2 || stats.SentBytes != 11 || stats.ReceivedBytes != 5 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestObservedTransportCarriesPathWeightsAndStats(t *testing.T) {
	observed := Observe(NewBundle(&observedStub{}, &observedStub{}, &observedStub{}), RouteInfo{
		Kind:        "relay",
		Connections: 3,
		PathWeights: []float64{1, 0.75, 1.25},
	})
	weights := SnapshotPhysicalPathWeights(observed)
	if len(weights) != 3 || weights[1] != 0.75 || weights[2] != 1.25 {
		t.Fatalf("weights = %#v", weights)
	}
	weights[1] = 99
	if SnapshotPhysicalPathWeights(observed)[1] != 0.75 {
		t.Fatal("path weights were returned without a defensive copy")
	}
	RecordPhysicalPathStats(observed, []PhysicalPathStats{{
		Connection: 1,
		SentBytes:  4096,
		SentChunks: 4,
		SendNanos:  int64(time.Millisecond),
	}})
	stats, ok := SnapshotRouteStats(observed)
	if !ok || len(stats.Paths) != 1 || stats.Paths[0].SentBytes != 4096 {
		t.Fatalf("route stats = %#v", stats)
	}
}
