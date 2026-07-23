package transport

import (
	"context"
	"errors"
	"testing"
)

type stubTransport struct {
	closed bool
	sent   [][]byte
}

func (s *stubTransport) Send(_ context.Context, payload []byte) error {
	s.sent = append(s.sent, append([]byte(nil), payload...))
	return nil
}

func (s *stubTransport) Recv(context.Context) ([]byte, error) {
	return nil, errors.New("not implemented")
}

func (s *stubTransport) Close() error {
	s.closed = true
	return nil
}

func TestBundleUsesControlChannelAndClosesAllChannels(t *testing.T) {
	control := &stubTransport{}
	data := &stubTransport{}
	bundle := NewBundle(control, data)
	if err := bundle.Send(context.Background(), []byte("control")); err != nil {
		t.Fatal(err)
	}
	if len(control.sent) != 1 || len(data.sent) != 0 {
		t.Fatalf("control sends=%d data sends=%d", len(control.sent), len(data.sent))
	}
	if got := len(Channels(bundle)); got != 2 {
		t.Fatalf("channels = %d, want 2", got)
	}
	if err := bundle.Close(); err != nil {
		t.Fatal(err)
	}
	if !control.closed || !data.closed {
		t.Fatal("bundle did not close every channel")
	}
}
