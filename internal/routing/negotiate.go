package routing

import (
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
)

const (
	NegotiationVersion = 1

	ProtocolTransfer = "transfer"
	ProtocolNote     = "note"

	ClientNative = "native"
	ClientWeb    = "web"
	ClientWebRTC = "webrtc"

	RouteNative = "native"
	RouteWebRTC = "webrtc"

	FeatureParallelData  = "parallel-data-v1"
	FeatureUnorderedData = "unordered-data-v1"
)

type Capability struct {
	Type         string   `json:"type"`
	Version      int      `json:"version"`
	Client       string   `json:"client"`
	Protocol     string   `json:"protocol,omitempty"`
	NativeRelay  string   `json:"native_relay,omitempty"`
	NativeLocal  bool     `json:"native_local,omitempty"`
	NativeDirect bool     `json:"native_direct,omitempty"`
	Features     []string `json:"features,omitempty"`
}

type Response struct {
	Type            string   `json:"type"`
	Version         int      `json:"version"`
	Route           string   `json:"route"`
	Pair            string   `json:"pair,omitempty"`
	Relay           string   `json:"relay,omitempty"`
	RelayCredential string   `json:"relay_credential,omitempty"`
	Direct          bool     `json:"direct,omitempty"`
	Local           bool     `json:"local,omitempty"`
	Reason          string   `json:"reason"`
	Features        []string `json:"features,omitempty"`
	Error           string   `json:"error,omitempty"`
}

func ValidateCapability(capability Capability) error {
	if capability.Type != "negotiate" || capability.Version != NegotiationVersion {
		return errors.New("unsupported negotiation protocol")
	}
	if capability.Client != ClientNative && capability.Client != ClientWeb && capability.Client != ClientWebRTC {
		return errors.New("invalid negotiation client")
	}
	if _, err := NormalizeProtocol(capability.Protocol); err != nil {
		return err
	}
	if capability.Client != ClientNative &&
		(capability.NativeRelay != "" || capability.NativeLocal || capability.NativeDirect) {
		return errors.New("non-native client advertised a native route")
	}
	if capability.NativeRelay != "" {
		return ValidateNativeRelay(capability.NativeRelay)
	}
	return nil
}

func ValidateNativeRelay(value string) error {
	value = strings.TrimSpace(value)
	host, port, err := net.SplitHostPort(value)
	if err != nil || strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
		return fmt.Errorf("native relay must be a host:port endpoint: %q", value)
	}
	return nil
}

func NormalizeProtocol(value string) (string, error) {
	switch value = strings.ToLower(strings.TrimSpace(value)); value {
	case "", ProtocolTransfer:
		return ProtocolTransfer, nil
	case ProtocolNote:
		return ProtocolNote, nil
	default:
		return "", fmt.Errorf("invalid room protocol %q", value)
	}
}

func Choose(sender, receiver Capability, serverRelay string) Response {
	response := Response{
		Type:     "negotiated",
		Version:  NegotiationVersion,
		Route:    RouteWebRTC,
		Pair:     Pair(sender.Client, receiver.Client),
		Reason:   "browser-or-webrtc-peer",
		Features: CommonFeatures(sender.Features, receiver.Features),
	}
	if sender.Client != ClientNative || receiver.Client != ClientNative {
		return response
	}
	if sender.NativeLocal || receiver.NativeLocal {
		if sender.NativeLocal && receiver.NativeLocal {
			response.Route = RouteNative
			response.Local = true
			response.Reason = "common-lan-discovery"
			return response
		}
		response.Reason = "no-common-native-route"
		return response
	}
	if relaysMatch(sender.NativeRelay, receiver.NativeRelay) {
		response.Route = RouteNative
		response.Relay = strings.TrimSpace(sender.NativeRelay)
		response.Direct = sender.NativeDirect && receiver.NativeDirect
		response.Reason = "common-native-relay"
		return response
	}
	if strings.TrimSpace(serverRelay) != "" {
		response.Route = RouteNative
		response.Relay = strings.TrimSpace(serverRelay)
		response.Direct = sender.NativeDirect && receiver.NativeDirect
		response.Reason = "service-native-relay"
		return response
	}
	if sender.NativeDirect && receiver.NativeDirect {
		response.Route = RouteNative
		response.Direct = true
		response.Reason = "signaling-direct"
		return response
	}
	response.Reason = "no-common-native-relay"
	return response
}

func CommonFeatures(left, right []string) []string {
	var common []string
	for _, supported := range []string{FeatureParallelData, FeatureUnorderedData} {
		if slices.Contains(left, supported) && slices.Contains(right, supported) {
			common = append(common, supported)
		}
	}
	return common
}

func Pair(left, right string) string {
	left = strings.ToLower(strings.TrimSpace(left))
	right = strings.ToLower(strings.TrimSpace(right))
	if left > right {
		left, right = right, left
	}
	if left == "" || right == "" {
		return ""
	}
	return left + "-" + right
}

func relaysMatch(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	return left != "" && strings.EqualFold(left, right)
}
