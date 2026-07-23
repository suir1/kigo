package relay

import (
	"context"
	"fmt"
	"time"

	"github.com/suir1/kigo/internal/transport"
)

const auxiliaryJoinTimeout = 5 * time.Second

func JoinBundle(ctx context.Context, primary JoinResult, addr string, base JoinOptions, count int) (transport.Transport, int) {
	count = min(count, normalizedRelayConnectionCount(primary.PeerConnectionCount))
	if count <= 1 {
		return primary.Transport, 1
	}
	channels := make([]transport.Transport, count)
	channels[0] = primary.Transport
	base.DialContext = primary.dialContext
	type outcome struct {
		index     int
		transport transport.Transport
		err       error
	}
	setupCtx, cancel := context.WithTimeout(ctx, auxiliaryJoinTimeout)
	defer cancel()
	results := make(chan outcome, count-1)
	for index := 1; index < count; index++ {
		index := index
		go func() {
			opts := base
			opts.Addr = addr
			opts.ConnectionIndex = index
			opts.ConnectionCount = count
			opts.Direct = ""
			opts.DirectCandidates = nil
			opts.UDPCandidates = nil
			opts.Capabilities = nil
			opts.DirectPreference = ""
			result, err := JoinWithOptions(setupCtx, opts)
			results <- outcome{index: index, transport: result.Transport, err: err}
		}()
	}
	var setupErr error
	for range count - 1 {
		result := <-results
		if result.err != nil {
			if setupErr == nil {
				setupErr = result.err
				cancel()
			}
			continue
		}
		channels[result.index] = result.transport
	}
	if setupErr != nil {
		for _, channel := range channels[1:] {
			if channel != nil {
				_ = channel.Close()
			}
		}
		return primary.Transport, 1
	}
	return transport.NewBundle(channels...), len(channels)
}

func ValidateConnectionCount(count int) error {
	if count < 1 || count > 8 {
		return fmt.Errorf("connections must be between 1 and 8")
	}
	return nil
}
