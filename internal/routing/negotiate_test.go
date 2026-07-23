package routing

import "testing"

func nativeCapability(relay string, local bool) Capability {
	return Capability{
		Type:         "negotiate",
		Version:      NegotiationVersion,
		Client:       ClientNative,
		NativeRelay:  relay,
		NativeLocal:  local,
		NativeDirect: true,
	}
}

func webCapability() Capability {
	return Capability{Type: "negotiate", Version: NegotiationVersion, Client: ClientWeb}
}

func TestChoose(t *testing.T) {
	tests := []struct {
		name        string
		sender      Capability
		receiver    Capability
		serverRelay string
		wantRoute   string
		wantRelay   string
		wantDirect  bool
		wantLocal   bool
		wantReason  string
	}{
		{"web pair", webCapability(), webCapability(), "relay.example:9000", RouteWebRTC, "", false, false, "browser-or-webrtc-peer"},
		{"matching relay", nativeCapability("relay.example:9000", false), nativeCapability("RELAY.example:9000", false), "", RouteNative, "relay.example:9000", true, false, "common-native-relay"},
		{"service relay", nativeCapability("one.example:9000", false), nativeCapability("two.example:9000", false), "service.example:9000", RouteNative, "service.example:9000", true, false, "service-native-relay"},
		{"common local", nativeCapability("", true), nativeCapability("", true), "service.example:9000", RouteNative, "", false, true, "common-lan-discovery"},
		{"one-sided local", nativeCapability("", true), nativeCapability("relay.example:9000", false), "service.example:9000", RouteWebRTC, "", false, false, "no-common-native-route"},
		{"direct only", nativeCapability("", false), nativeCapability("", false), "", RouteNative, "", true, false, "signaling-direct"},
		{"no common route", Capability{Type: "negotiate", Version: NegotiationVersion, Client: ClientNative}, nativeCapability("", false), "", RouteWebRTC, "", false, false, "no-common-native-relay"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Choose(tt.sender, tt.receiver, tt.serverRelay)
			if got.Route != tt.wantRoute || got.Pair != Pair(tt.sender.Client, tt.receiver.Client) ||
				got.Relay != tt.wantRelay || got.Direct != tt.wantDirect || got.Local != tt.wantLocal ||
				got.Reason != tt.wantReason {
				t.Fatalf("route = %#v", got)
			}
		})
	}
}

func TestChooseReturnsCommonFeatures(t *testing.T) {
	sender := webCapability()
	sender.Features = []string{FeatureUnorderedData, FeatureParallelData, "sender-only"}
	receiver := webCapability()
	receiver.Features = []string{"receiver-only", FeatureParallelData, FeatureUnorderedData}

	response := Choose(sender, receiver, "")
	if len(response.Features) != 2 || response.Features[0] != FeatureParallelData || response.Features[1] != FeatureUnorderedData {
		t.Fatalf("features = %#v", response.Features)
	}

	receiver.Features = nil
	if response = Choose(sender, receiver, ""); len(response.Features) != 0 {
		t.Fatalf("one-sided features = %#v", response.Features)
	}
}

func TestValidateCapability(t *testing.T) {
	tests := []Capability{
		{Type: "bad", Version: NegotiationVersion, Client: ClientNative},
		{Type: "negotiate", Version: 99, Client: ClientNative},
		{Type: "negotiate", Version: NegotiationVersion, Client: "bad"},
		{Type: "negotiate", Version: NegotiationVersion, Client: ClientWeb, NativeLocal: true},
		{Type: "negotiate", Version: NegotiationVersion, Client: ClientWeb, NativeDirect: true},
		{Type: "negotiate", Version: NegotiationVersion, Client: ClientNative, NativeRelay: "bad"},
		{Type: "negotiate", Version: NegotiationVersion, Client: ClientNative, Protocol: "bad"},
	}
	for _, capability := range tests {
		if err := ValidateCapability(capability); err == nil {
			t.Fatalf("capability accepted: %#v", capability)
		}
	}

	valid := nativeCapability("relay.example:9000", false)
	valid.Protocol = ProtocolNote
	if err := ValidateCapability(valid); err != nil {
		t.Fatalf("valid capability rejected: %v", err)
	}
}
