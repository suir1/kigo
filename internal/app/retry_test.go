package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/suir1/kigo/internal/transfer"
	"github.com/suir1/kigo/internal/transport"
)

func TestRunTransferWithReconnectRetriesNetworkFailure(t *testing.T) {
	var dials int
	g := &globalOptions{
		Relay:             "relay.test:9000",
		ReconnectAttempts: 3,
		ReconnectDelay:    time.Millisecond,
	}
	err := runTransferWithReconnect(
		context.Background(),
		g,
		func(context.Context) (transport.Transport, error) {
			dials++
			return &stubTransport{}, nil
		},
		func(context.Context, transport.Transport) error {
			if dials == 1 {
				return io.ErrUnexpectedEOF
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if dials != 2 {
		t.Fatalf("dials = %d", dials)
	}
}

func TestRunTransferWithReconnectDoesNotRetryIntegrityFailure(t *testing.T) {
	var dials int
	g := &globalOptions{
		Relay:             "relay.test:9000",
		ReconnectAttempts: 3,
		ReconnectDelay:    time.Millisecond,
	}
	err := runTransferWithReconnect(
		context.Background(),
		g,
		func(context.Context) (transport.Transport, error) {
			dials++
			return &stubTransport{}, nil
		},
		func(context.Context, transport.Transport) error {
			return errors.New("sha256 mismatch for data.bin")
		},
	)
	if err == nil {
		t.Fatal("integrity failure was accepted")
	}
	if dials != 1 {
		t.Fatalf("dials = %d", dials)
	}
}

func TestRunTransferWithReconnectGateEnablesNegotiatedWebRTCRetry(t *testing.T) {
	var dials int
	g := &globalOptions{
		ReconnectAttempts: 3,
		ReconnectDelay:    time.Millisecond,
	}
	err := runTransferWithReconnectGate(
		context.Background(),
		g,
		func() bool { return true },
		func(context.Context) (transport.Transport, error) {
			dials++
			return &stubTransport{}, nil
		},
		func(context.Context, transport.Transport) error {
			if dials == 1 {
				return transport.ErrClosed
			}
			return nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if dials != 2 {
		t.Fatalf("dials = %d, want 2", dials)
	}
}

func TestRunTransferWithReconnectGateKeepsLegacyWebRTCOneShot(t *testing.T) {
	var dials int
	g := &globalOptions{
		ReconnectAttempts: 3,
		ReconnectDelay:    time.Millisecond,
	}
	err := runTransferWithReconnectGate(
		context.Background(),
		g,
		func() bool { return false },
		func(context.Context) (transport.Transport, error) {
			dials++
			return &stubTransport{}, nil
		},
		func(context.Context, transport.Transport) error {
			return transport.ErrClosed
		},
	)
	if !errors.Is(err, transport.ErrClosed) {
		t.Fatalf("error = %v, want transport.ErrClosed", err)
	}
	if dials != 1 {
		t.Fatalf("dials = %d, want 1", dials)
	}
}

func TestAutoReconnectResumesInterruptedEncryptedFileTransfer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	sourceDir := t.TempDir()
	outputDir := t.TempDir()
	sourcePath := filepath.Join(sourceDir, "payload.bin")
	want := bytes.Repeat([]byte("kigo reconnect payload\n"), 60000)
	if err := os.WriteFile(sourcePath, want, 0o600); err != nil {
		t.Fatal(err)
	}
	prepared, err := transfer.PreparePath(sourcePath)
	if err != nil {
		t.Fatal(err)
	}

	firstSender, firstReceiver := net.Pipe()
	secondSender, secondReceiver := net.Pipe()
	var senderDials, receiverDials int
	var resumed atomic.Bool
	g := &globalOptions{
		Relay:             "relay.test:9000",
		ReconnectAttempts: 3,
		ReconnectDelay:    5 * time.Millisecond,
	}
	senderDial := func(context.Context) (transport.Transport, error) {
		senderDials++
		switch senderDials {
		case 1:
			base := transport.NewTCPTransport(firstSender)
			return &dropAfterSendTransport{base: base, limit: 6}, nil
		case 2:
			return transport.NewTCPTransport(secondSender), nil
		default:
			return nil, errors.New("unexpected sender dial")
		}
	}
	receiverDial := func(context.Context) (transport.Transport, error) {
		receiverDials++
		switch receiverDials {
		case 1:
			return transport.NewTCPTransport(firstReceiver), nil
		case 2:
			return transport.NewTCPTransport(secondReceiver), nil
		default:
			return nil, errors.New("unexpected receiver dial")
		}
	}

	senderErr := make(chan error, 1)
	go func() {
		senderErr <- runTransferWithReconnect(
			ctx,
			g,
			senderDial,
			func(ctx context.Context, t transport.Transport) error {
				return prepared.Send(ctx, t, transfer.SenderOptions{Code: "ABC123"})
			},
		)
	}()
	receiverErr := runTransferWithReconnect(
		ctx,
		g,
		receiverDial,
		func(ctx context.Context, t transport.Transport) error {
			_, err := transfer.Receive(ctx, t, transfer.ReceiverOptions{
				Code:      "ABC123",
				OutputDir: outputDir,
				Logf: func(format string, args ...any) {
					if strings.Contains(fmt.Sprintf(format, args...), "resuming payload.bin from") {
						resumed.Store(true)
					}
				},
			})
			return err
		},
	)
	if receiverErr != nil {
		t.Fatal(receiverErr)
	}
	if err := <-senderErr; err != nil {
		t.Fatal(err)
	}
	if senderDials != 2 || receiverDials != 2 {
		t.Fatalf("sender dials=%d receiver dials=%d", senderDials, receiverDials)
	}
	if !resumed.Load() {
		t.Fatal("second transfer attempt did not resume the partial file")
	}
	got, err := os.ReadFile(filepath.Join(outputDir, "payload.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("resumed payload mismatch: got %d want %d", len(got), len(want))
	}
}

type stubTransport struct{}

func (s *stubTransport) Send(context.Context, []byte) error { return nil }
func (s *stubTransport) Recv(context.Context) ([]byte, error) {
	return nil, io.EOF
}
func (s *stubTransport) Close() error { return nil }

type dropAfterSendTransport struct {
	base  transport.Transport
	limit int32
	sends atomic.Int32
}

func (t *dropAfterSendTransport) Send(ctx context.Context, payload []byte) error {
	if t.sends.Add(1) > t.limit {
		_ = t.base.Close()
		return io.ErrUnexpectedEOF
	}
	return t.base.Send(ctx, payload)
}

func (t *dropAfterSendTransport) Recv(ctx context.Context) ([]byte, error) {
	return t.base.Recv(ctx)
}

func (t *dropAfterSendTransport) Close() error {
	return t.base.Close()
}
