package app

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/suir1/kigo/internal/routing"
)

const (
	transportModeAuto   = "auto"
	transportModeNative = routing.RouteNative
	transportModeWebRTC = routing.RouteWebRTC
	negotiationVersion  = routing.NegotiationVersion
	protocolTransfer    = routing.ProtocolTransfer
	protocolNote        = routing.ProtocolNote
)

type negotiationCapability = routing.Capability
type negotiationResponse = routing.Response

func normalizeTransportMode(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		value = transportModeAuto
	}
	switch value {
	case transportModeAuto, transportModeNative, transportModeWebRTC:
		return value, nil
	default:
		return "", fmt.Errorf("invalid --transport %q; use auto, native, or webrtc", value)
	}
}

func resolveTransferOptions(
	ctx context.Context,
	g *globalOptions,
	roomToken string,
	role string,
) (*globalOptions, error) {
	return resolveTransportOptions(ctx, g, roomToken, role, protocolTransfer)
}

func resolveNoteOptions(
	ctx context.Context,
	g *globalOptions,
	roomToken string,
	role string,
) (*globalOptions, error) {
	return resolveTransportOptions(ctx, g, roomToken, role, protocolNote)
}

func resolveTransportOptions(
	ctx context.Context,
	g *globalOptions,
	roomToken string,
	role string,
	protocol string,
) (*globalOptions, error) {
	mode, err := normalizeTransportMode(g.Transport)
	if err != nil {
		return nil, err
	}
	switch mode {
	case transportModeWebRTC:
		resolved := webRTCOptions(g)
		resolved.Protocol = protocol
		taskLogf(g, "Route negotiation: WebRTC (forced)")
		return resolved, nil
	case transportModeNative:
		if g.Relay == "" && !g.Local && nativeDirectDisabled(g) {
			return nil, errors.New("--transport native requires direct TCP, --relay, or --local")
		}
		resolved := cloneGlobalOptions(g)
		resolved.SignalDirect = !g.Local && !nativeDirectDisabled(g)
		resolved.Protocol = protocol
		taskLogf(g, "Route negotiation: native (forced)")
		return resolved, nil
	}

	response, err := negotiateTransportRoute(ctx, g, roomToken, role, protocol)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if g.Relay != "" || g.Local {
			taskLogf(g, "Route negotiation unavailable: %v; using configured native route", err)
			resolved := cloneGlobalOptions(g)
			resolved.Protocol = protocol
			return resolved, nil
		}
		taskLogf(g, "Route negotiation unavailable: %v; using WebRTC", err)
		resolved := webRTCOptions(g)
		resolved.Protocol = protocol
		return resolved, nil
	}
	switch response.Route {
	case transportModeNative:
		resolved := cloneGlobalOptions(g)
		resolved.Relay = response.Relay
		resolved.Local = response.Local
		resolved.SignalDirect = response.Direct && !nativeDirectDisabled(resolved)
		resolved.Protocol = protocol
		if response.RelayCredential != "" {
			resolved.RelayPass = response.RelayCredential
		}
		if resolved.Relay == "" && !resolved.Local && !resolved.SignalDirect {
			return nil, errors.New("negotiation selected native without a direct, relay, or LAN route")
		}
		taskLogf(g, "Route negotiation: native (%s)", response.Reason)
		if resolved.Relay != "" {
			taskLogf(g, "Negotiated relay: %s", resolved.Relay)
		}
		if response.RelayCredential != "" {
			taskLogf(g, "Negotiated relay credential: temporary")
		}
		return resolved, nil
	case transportModeWebRTC:
		taskLogf(g, "Route negotiation: WebRTC (%s)", response.Reason)
		resolved := webRTCOptions(g)
		resolved.Protocol = protocol
		return resolved, nil
	default:
		return nil, fmt.Errorf("negotiation selected unsupported route %q", response.Route)
	}
}

func cloneGlobalOptions(g *globalOptions) *globalOptions {
	if g == nil {
		return &globalOptions{}
	}
	clone := *g
	return &clone
}

func webRTCOptions(g *globalOptions) *globalOptions {
	clone := cloneGlobalOptions(g)
	clone.Relay = ""
	clone.Local = false
	clone.SignalDirect = false
	return clone
}

func negotiateTransferRoute(
	ctx context.Context,
	g *globalOptions,
	roomToken string,
	role string,
) (negotiationResponse, error) {
	return negotiateTransportRoute(ctx, g, roomToken, role, protocolTransfer)
}

func negotiateTransportRoute(
	ctx context.Context,
	g *globalOptions,
	roomToken string,
	role string,
	protocol string,
) (negotiationResponse, error) {
	if !isSignalRoleClient(role) {
		return negotiationResponse{}, errors.New("invalid negotiation role")
	}
	endpoint, err := negotiationURL(g.Signal, roomToken, role)
	if err != nil {
		return negotiationResponse{}, err
	}
	conn, response, err := outboundWebSocketDialer(g).DialContext(ctx, endpoint, nil)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		return negotiationResponse{}, fmt.Errorf("connect transport negotiation: %w", err)
	}
	defer conn.Close()
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stop:
		}
	}()
	capability := negotiationCapability{
		Type:         "negotiate",
		Version:      negotiationVersion,
		Client:       routing.ClientNative,
		Protocol:     protocol,
		NativeRelay:  strings.TrimSpace(g.Relay),
		NativeLocal:  g.Local,
		NativeDirect: !nativeDirectDisabled(g),
	}
	if err := conn.WriteJSON(capability); err != nil {
		return negotiationResponse{}, fmt.Errorf("send transport capability: %w", err)
	}
	var result negotiationResponse
	if err := conn.ReadJSON(&result); err != nil {
		return negotiationResponse{}, fmt.Errorf("read transport negotiation: %w", err)
	}
	if result.Type == "error" {
		if result.Error == "" {
			result.Error = "transport negotiation failed"
		}
		return negotiationResponse{}, errors.New(result.Error)
	}
	if result.Type != "negotiated" || result.Version != negotiationVersion {
		return negotiationResponse{}, errors.New("unsupported transport negotiation response")
	}
	return result, nil
}

func nativeDirectDisabled(g *globalOptions) bool {
	return g == nil || g.NoDirect || strings.TrimSpace(g.Proxy) != ""
}

func negotiationURL(base, roomToken, role string) (string, error) {
	httpURL, err := apiURL(base, "/api/negotiate/"+roomToken)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(httpURL)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("signaling URL must use http or https: %q", base)
	}
	query := u.Query()
	query.Set("role", role)
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func isSignalRoleClient(role string) bool {
	return role == "sender" || role == "receiver"
}
