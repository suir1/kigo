package transport

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

var ErrClosed = errors.New("transport closed")

type Transport interface {
	Send(ctx context.Context, payload []byte) error
	Recv(ctx context.Context) ([]byte, error)
	Close() error
}

type ChannelProvider interface {
	Channels() []Transport
}

type Bundle struct {
	channels []Transport
	close    sync.Once
	closeErr error
}

func NewBundle(channels ...Transport) *Bundle {
	filtered := make([]Transport, 0, len(channels))
	for _, channel := range channels {
		if channel != nil {
			filtered = append(filtered, channel)
		}
	}
	return &Bundle{channels: filtered}
}

func Channels(t Transport) []Transport {
	if provider, ok := t.(ChannelProvider); ok {
		channels := provider.Channels()
		if len(channels) > 0 {
			return channels
		}
	}
	if t == nil {
		return nil
	}
	return []Transport{t}
}

func (b *Bundle) Channels() []Transport {
	if b == nil {
		return nil
	}
	return append([]Transport(nil), b.channels...)
}

func (b *Bundle) Send(ctx context.Context, payload []byte) error {
	if b == nil || len(b.channels) == 0 {
		return ErrClosed
	}
	return b.channels[0].Send(ctx, payload)
}

func (b *Bundle) Recv(ctx context.Context) ([]byte, error) {
	if b == nil || len(b.channels) == 0 {
		return nil, ErrClosed
	}
	return b.channels[0].Recv(ctx)
}

func (b *Bundle) Close() error {
	if b == nil {
		return nil
	}
	b.close.Do(func() {
		for _, channel := range b.channels {
			if err := channel.Close(); err != nil && b.closeErr == nil && !errors.Is(err, net.ErrClosed) {
				b.closeErr = err
			}
		}
	})
	return b.closeErr
}

type SendMetrics struct {
	BufferedBytes uint64
	BufferLimit   uint64
	LastWait      time.Duration
}

type SendMetricsProvider interface {
	SendMetrics() SendMetrics
}

func SnapshotSendMetrics(t Transport) SendMetrics {
	if provider, ok := t.(SendMetricsProvider); ok {
		return provider.SendMetrics()
	}
	return SendMetrics{}
}

func SnapshotBundleSendMetrics(t Transport) SendMetrics {
	var combined SendMetrics
	for _, channel := range Channels(t) {
		metrics := SnapshotSendMetrics(channel)
		combined.BufferedBytes += metrics.BufferedBytes
		combined.BufferLimit += metrics.BufferLimit
		if metrics.LastWait > combined.LastWait {
			combined.LastWait = metrics.LastWait
		}
	}
	return combined
}

func AdaptiveSendBudget(maxBytes, minBytes int, metrics SendMetrics) int {
	if maxBytes <= 0 {
		return 0
	}
	if minBytes <= 0 || minBytes > maxBytes {
		minBytes = maxBytes
	}
	budget := maxBytes
	if metrics.BufferLimit > 0 {
		quarter := metrics.BufferLimit / 4
		if metrics.BufferLimit%4 != 0 {
			quarter++
		}
		half := metrics.BufferLimit / 2
		if metrics.BufferLimit%2 != 0 {
			half++
		}
		threeQuarter := metrics.BufferLimit - metrics.BufferLimit/4
		switch {
		case metrics.BufferedBytes >= threeQuarter:
			budget = minBytes
		case metrics.BufferedBytes >= half:
			budget = max(maxBytes/4, minBytes)
		case metrics.BufferedBytes >= quarter:
			budget = max(maxBytes/2, minBytes)
		}
	}
	switch {
	case metrics.LastWait >= 100*time.Millisecond:
		budget = minBytes
	case metrics.LastWait >= 20*time.Millisecond:
		budget = min(budget, max(maxBytes/4, minBytes))
	case metrics.LastWait >= 5*time.Millisecond:
		budget = min(budget, max(maxBytes/2, minBytes))
	}
	return max(min(budget, maxBytes), minBytes)
}

type TCPTransport struct {
	conn net.Conn
}

func NewTCPTransport(conn net.Conn) *TCPTransport {
	return &TCPTransport{conn: conn}
}

func (t *TCPTransport) Send(ctx context.Context, payload []byte) error {
	if deadline, ok := ctx.Deadline(); ok {
		_ = t.conn.SetWriteDeadline(deadline)
	} else {
		_ = t.conn.SetWriteDeadline(time.Time{})
	}
	return writeFrame(t.conn, payload)
}

func (t *TCPTransport) Recv(ctx context.Context) ([]byte, error) {
	if deadline, ok := ctx.Deadline(); ok {
		_ = t.conn.SetReadDeadline(deadline)
	} else {
		_ = t.conn.SetReadDeadline(time.Time{})
	}
	return readFrame(t.conn)
}

func (t *TCPTransport) Close() error {
	return t.conn.Close()
}
