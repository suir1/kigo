package transfer

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/suir1/kigo/internal/mux"
	"github.com/suir1/kigo/internal/protocol"
	"github.com/suir1/kigo/internal/secure"
	"github.com/suir1/kigo/internal/transport"
)

type pathTestTransport struct {
	delay     time.Duration
	failAfter int
	mu        sync.Mutex
	payloads  [][]byte
}

func (t *pathTestTransport) Send(ctx context.Context, payload []byte) error {
	if t.delay > 0 {
		timer := time.NewTimer(t.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failAfter >= 0 && len(t.payloads) >= t.failAfter {
		return errors.New("path send failed")
	}
	t.payloads = append(t.payloads, append([]byte(nil), payload...))
	return nil
}

func (t *pathTestTransport) Recv(context.Context) ([]byte, error) {
	return nil, errors.New("receive not implemented")
}

func (t *pathTestTransport) Close() error {
	return nil
}

func (t *pathTestTransport) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.payloads)
}

func (t *pathTestTransport) envelopes(tester *testing.T) []envelope {
	tester.Helper()
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]envelope, len(t.payloads))
	for i, payload := range t.payloads {
		if err := json.Unmarshal(payload, &out[i]); err != nil {
			tester.Fatal(err)
		}
	}
	return out
}

func TestParallelChunkSenderFavorsLessBackloggedPath(t *testing.T) {
	slow := &pathTestTransport{delay: 5 * time.Millisecond, failAfter: -1}
	fast := &pathTestTransport{failAfter: -1}
	session := newParallelSenderTestSession(t, slow, fast)
	sender := newParallelChunkSender(context.Background(), session)
	payload := make([]byte, 1024)
	for index := range 80 {
		if err := sender.SendChunk(context.Background(), 0, int64(index*len(payload)), payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := sender.Close(); err != nil {
		t.Fatal(err)
	}
	if fast.count() <= slow.count() {
		t.Fatalf("fast path sends=%d slow path sends=%d", fast.count(), slow.count())
	}
	for connection, path := range []*pathTestTransport{slow, fast} {
		for index, env := range path.envelopes(t) {
			if env.Seq != uint64(index) {
				t.Fatalf("connection %d sequence=%d want=%d", connection+1, env.Seq, index)
			}
		}
	}
}

func TestParallelChunkSenderUsesHistoricalPathWeights(t *testing.T) {
	lowerWeight := &pathTestTransport{delay: 20 * time.Millisecond, failAfter: -1}
	higherWeight := &pathTestTransport{delay: 20 * time.Millisecond, failAfter: -1}
	session := newParallelSenderTestSession(t, lowerWeight, higherWeight)
	session.pathWeights = []float64{1, 0.5, 2}
	sender := newParallelChunkSender(context.Background(), session)
	payload := make([]byte, 1024)
	for index := range 8 {
		if err := sender.SendChunk(context.Background(), 0, int64(index*len(payload)), payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := sender.Close(); err != nil {
		t.Fatal(err)
	}
	if higherWeight.count() <= lowerWeight.count()*2 {
		t.Fatalf(
			"historical weights were not reflected: high=%d low=%d",
			higherWeight.count(),
			lowerWeight.count(),
		)
	}
}

func TestParallelChunkSenderPropagatesPathFailure(t *testing.T) {
	failing := &pathTestTransport{failAfter: 0}
	other := &pathTestTransport{delay: time.Millisecond, failAfter: -1}
	session := newParallelSenderTestSession(t, failing, other)
	sender := newParallelChunkSender(context.Background(), session)
	payload := make([]byte, 1024)
	for index := range 20 {
		err := sender.SendChunk(context.Background(), 0, int64(index*len(payload)), payload)
		if err != nil {
			break
		}
	}
	if err := sender.Close(); err == nil || err.Error() != "path send failed" {
		t.Fatalf("close error = %v", err)
	}
}

func newParallelSenderTestSession(t *testing.T, paths ...transport.Transport) *TransferSession {
	t.Helper()
	pipes := make([]*securePipe, len(paths)+1)
	for index := range pipes {
		sendSession, err := secure.NewSessionWithInfo(
			"ABC123",
			"sender",
			"receiver",
			channelKeyInfo("sender-to-receiver", index),
		)
		if err != nil {
			t.Fatal(err)
		}
		tp := transport.Transport(&pathTestTransport{failAfter: -1})
		if index > 0 {
			tp = paths[index-1]
		}
		pipes[index] = &securePipe{
			t:           tp,
			index:       index,
			sendSession: sendSession,
			striping:    true,
		}
	}
	session := newTransferSessionWithPipes(pipes, false)
	manifest := protocol.NewManifest([]protocol.Item{{
		Kind:      protocol.ItemFile,
		Name:      "data.bin",
		Size:      1 << 30,
		ChunkSize: protocol.ChunkSize,
	}})
	plan := mux.NewPlan(1)
	plan.Apply(&manifest)
	session.sendManifest = &manifest
	session.sendPlan = plan
	return session
}
