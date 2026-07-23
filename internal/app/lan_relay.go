package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/suir1/kigo/internal/directcandidate"
	"github.com/suir1/kigo/internal/discovery"
	"github.com/suir1/kigo/internal/relay"
	"github.com/suir1/kigo/internal/transport"
)

const (
	lanUpgradeVersion      = 1
	lanUpgradeTimeout      = 2 * time.Second
	maxLANUpgradeMessage   = 16 << 10
	lanUpgradeRequestType  = "lan_upgrade_request"
	lanUpgradeResponseType = "lan_upgrade_response"
)

type embeddedLANRelay struct {
	Addr   string
	cancel context.CancelFunc
	once   sync.Once
}

type lanUpgradeMessage struct {
	Type              string                      `json:"type"`
	Version           int                         `json:"version"`
	Candidates        []string                    `json:"candidates,omitempty"`
	CandidateMetadata []directcandidate.Candidate `json:"candidate_meta,omitempty"`
	Error             string                      `json:"error,omitempty"`
}

type cleanupTransport struct {
	inner   transport.Transport
	cleanup func()
	once    sync.Once
}

func startEmbeddedLANRelay(ctx context.Context, g *globalOptions) (*embeddedLANRelay, error) {
	listen := "0.0.0.0:0"
	if policy := selectedNetworkPolicy(g); policy != nil {
		if ip := policy.IPv4(); ip != nil {
			listen = net.JoinHostPort(ip.String(), "0")
		}
	}
	listener, err := net.Listen("tcp4", listen)
	if err != nil {
		return nil, fmt.Errorf("start embedded LAN relay: %w", err)
	}
	port := directListenerPort(listener)
	if port == 0 {
		_ = listener.Close()
		return nil, errors.New("embedded LAN relay has no usable TCP port")
	}
	host, _, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("embedded LAN relay address: %w", err)
	}
	if ip := net.ParseIP(host); ip == nil || ip.IsUnspecified() {
		host = "127.0.0.1"
	}

	// The pairing context is canceled as soon as dialing succeeds. The relay is
	// instead stopped by the returned transport's Close method.
	serverCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	embedded := &embeddedLANRelay{
		Addr:   net.JoinHostPort(host, fmt.Sprint(port)),
		cancel: cancel,
	}
	go func() {
		_ = relay.NewServer().Serve(serverCtx, listener)
	}()
	go func() {
		_ = discovery.AnnounceOnInterface(serverCtx, g.DiscoveryAddr, port, g.Interface)
	}()
	return embedded, nil
}

func (relay *embeddedLANRelay) Stop() {
	if relay == nil {
		return
	}
	relay.once.Do(relay.cancel)
}

func (t *cleanupTransport) Send(ctx context.Context, payload []byte) error {
	return t.inner.Send(ctx, payload)
}

func (t *cleanupTransport) Recv(ctx context.Context) ([]byte, error) {
	return t.inner.Recv(ctx)
}

func (t *cleanupTransport) Close() error {
	err := t.inner.Close()
	t.once.Do(t.cleanup)
	return err
}

func (t *cleanupTransport) Channels() []transport.Transport {
	return transport.Channels(t.inner)
}

func withTransportCleanup(t transport.Transport, cleanup func()) transport.Transport {
	if t == nil || cleanup == nil {
		return t
	}
	return &cleanupTransport{inner: t, cleanup: cleanup}
}

func finalizeRelayRoute(
	ctx context.Context,
	g *globalOptions,
	roomToken string,
	role string,
	result relay.RaceResult,
	join relay.JoinOptions,
	embedded *embeddedLANRelay,
) (transport.Transport, int, string, string, bool) {
	if shouldAttemptLANUpgrade(g, result, join) {
		upgraded, count, err := tryLANUpgrade(ctx, g, roomToken, role, result.Transport)
		if err == nil {
			_ = result.Transport.Close()
			if embedded != nil {
				embedded.Stop()
			}
			return upgraded, count, historyRouteDirect, "LAN direct upgrade", false
		}
		taskLinef(g, "LAN upgrade unavailable: %v", err)
	}

	bundle, count := relay.JoinBundle(ctx, result.JoinResult, result.Candidate.Addr, join, g.Connections)
	label := "relay"
	keepEmbedded := false
	switch result.Candidate.Kind {
	case "embedded":
		label = "embedded LAN relay"
		if embedded != nil {
			bundle = withTransportCleanup(bundle, embedded.Stop)
			keepEmbedded = true
		}
	case "lan":
		label = "LAN relay"
	}
	if embedded != nil && !keepEmbedded {
		embedded.Stop()
	}
	return bundle, count, historyRouteRelay, label, keepEmbedded
}

func shouldAttemptLANUpgrade(g *globalOptions, result relay.RaceResult, join relay.JoinOptions) bool {
	return g != nil &&
		lanUpgradeEnabled(g) &&
		result.Candidate.Kind == "external" &&
		hasCapability(join.Capabilities, relay.CapabilityLANUpgradeV1) &&
		hasCapability(result.PeerCapabilities, relay.CapabilityLANUpgradeV1)
}

func lanUpgradeEnabled(g *globalOptions) bool {
	return g != nil &&
		!g.NoLAN &&
		strings.TrimSpace(g.Proxy) == "" &&
		(!g.NoDirect || g.allowLANUpgrade)
}

func tryLANUpgrade(
	ctx context.Context,
	g *globalOptions,
	roomToken string,
	role string,
	relayTransport transport.Transport,
) (transport.Transport, int, error) {
	timeout := lanUpgradeTimeout
	if g != nil && g.DirectTimeout > 0 {
		// One peer may finish the preceding direct race before the other. Keep
		// the relay control phase open long enough for that skew to settle.
		timeout += g.DirectTimeout
	}
	upgradeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	switch role {
	case "sender":
		return tryLANUpgradeSender(upgradeCtx, g, roomToken, relayTransport)
	case "receiver":
		return tryLANUpgradeReceiver(upgradeCtx, g, roomToken, relayTransport)
	default:
		return nil, 0, fmt.Errorf("invalid LAN upgrade role %q", role)
	}
}

func tryLANUpgradeSender(
	ctx context.Context,
	g *globalOptions,
	roomToken string,
	relayTransport transport.Transport,
) (transport.Transport, int, error) {
	request, err := recvLANUpgradeMessage(ctx, relayTransport)
	if err != nil {
		return nil, 0, err
	}
	if request.Type != lanUpgradeRequestType || request.Version != lanUpgradeVersion {
		return nil, 0, errors.New("invalid LAN upgrade request")
	}
	listener, err := listenDirect(ctx, directListenAddress(g))
	if err != nil {
		_ = sendLANUpgradeMessage(ctx, relayTransport, lanUpgradeMessage{
			Type: lanUpgradeResponseType, Version: lanUpgradeVersion, Error: err.Error(),
		})
		return nil, 0, err
	}
	lanOptions := cloneGlobalOptions(g)
	lanOptions.DirectAdvertise = ""
	candidates, metadata := advertisedDirectCandidateSet(lanOptions, listener.Addr())
	if len(candidates) == 0 {
		_ = listener.Close()
		err := errors.New("LAN upgrade listener has no candidates")
		_ = sendLANUpgradeMessage(ctx, relayTransport, lanUpgradeMessage{
			Type: lanUpgradeResponseType, Version: lanUpgradeVersion, Error: err.Error(),
		})
		return nil, 0, err
	}
	if err := sendLANUpgradeMessage(ctx, relayTransport, lanUpgradeMessage{
		Type:              lanUpgradeResponseType,
		Version:           lanUpgradeVersion,
		Candidates:        candidates,
		CandidateMetadata: metadata,
	}); err != nil {
		_ = listener.Close()
		return nil, 0, err
	}
	return acceptDirectBundle(ctx, listener, remainingLANUpgradeTime(ctx), roomToken, g.Connections)
}

func tryLANUpgradeReceiver(
	ctx context.Context,
	g *globalOptions,
	roomToken string,
	relayTransport transport.Transport,
) (transport.Transport, int, error) {
	if err := sendLANUpgradeMessage(ctx, relayTransport, lanUpgradeMessage{
		Type: lanUpgradeRequestType, Version: lanUpgradeVersion,
	}); err != nil {
		return nil, 0, err
	}
	response, err := recvLANUpgradeMessage(ctx, relayTransport)
	if err != nil {
		return nil, 0, err
	}
	if response.Type != lanUpgradeResponseType || response.Version != lanUpgradeVersion {
		return nil, 0, errors.New("invalid LAN upgrade response")
	}
	if response.Error != "" {
		return nil, 0, errors.New(response.Error)
	}
	if err := directcandidate.ValidateSet(response.Candidates, response.CandidateMetadata); err != nil {
		return nil, 0, err
	}
	return connectDirectBundle(ctx, directConnectOptions{
		candidates:  response.Candidates,
		metadata:    response.CandidateMetadata,
		timeout:     remainingLANUpgradeTime(ctx),
		roomToken:   roomToken,
		connections: g.Connections,
		dialContext: outboundDirectDialer(g),
	})
}

func sendLANUpgradeMessage(ctx context.Context, t transport.Transport, message lanUpgradeMessage) error {
	payload, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(payload) == 0 || len(payload) > maxLANUpgradeMessage {
		return errors.New("LAN upgrade message is too large")
	}
	return t.Send(ctx, payload)
}

func recvLANUpgradeMessage(ctx context.Context, t transport.Transport) (lanUpgradeMessage, error) {
	payload, err := t.Recv(ctx)
	if err != nil {
		return lanUpgradeMessage{}, err
	}
	if len(payload) == 0 || len(payload) > maxLANUpgradeMessage {
		return lanUpgradeMessage{}, errors.New("invalid LAN upgrade message size")
	}
	var message lanUpgradeMessage
	if err := json.Unmarshal(payload, &message); err != nil {
		return lanUpgradeMessage{}, err
	}
	return message, nil
}

func remainingLANUpgradeTime(ctx context.Context) time.Duration {
	deadline, ok := ctx.Deadline()
	if !ok {
		return lanUpgradeTimeout
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		return time.Nanosecond
	}
	return remaining
}
