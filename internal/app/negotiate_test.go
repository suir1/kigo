package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const appNegotiationTestToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestNormalizeTransportMode(t *testing.T) {
	for input, want := range map[string]string{
		"":        transportModeAuto,
		"AUTO":    transportModeAuto,
		" native": transportModeNative,
		"webrtc ": transportModeWebRTC,
	} {
		got, err := normalizeTransportMode(input)
		if err != nil || got != want {
			t.Fatalf("normalizeTransportMode(%q) = %q, %v", input, got, err)
		}
	}
	if _, err := normalizeTransportMode("tcp"); err == nil {
		t.Fatal("invalid transport mode was accepted")
	}
}

func TestResolveTransferOptionsForcedWebRTCClonesAndStripsNativeRoutes(t *testing.T) {
	original := &globalOptions{
		Transport: transportModeWebRTC,
		Relay:     "relay.example:9000",
		Local:     true,
		NoDirect:  true,
	}
	resolved, err := resolveTransferOptions(
		context.Background(),
		original,
		appNegotiationTestToken,
		"sender",
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved == original {
		t.Fatal("options were not cloned")
	}
	if resolved.Relay != "" || resolved.Local {
		t.Fatalf("resolved = %#v", resolved)
	}
	if original.Relay != "relay.example:9000" || !original.Local {
		t.Fatalf("original options were mutated: %#v", original)
	}
}

func TestResolveNoteOptionsMarksProtocol(t *testing.T) {
	resolved, err := resolveNoteOptions(
		context.Background(),
		&globalOptions{
			Transport: transportModeWebRTC,
			Relay:     "relay.example:9000",
			Local:     true,
		},
		appNegotiationTestToken,
		"sender",
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Protocol != protocolNote || resolved.Relay != "" || resolved.Local {
		t.Fatalf("resolved note options = %#v", resolved)
	}
}

func TestResolveTransferOptionsForcedNativeRequiresRoute(t *testing.T) {
	direct, err := resolveTransferOptions(
		context.Background(),
		&globalOptions{Transport: transportModeNative},
		appNegotiationTestToken,
		"receiver",
	)
	if err != nil || !direct.SignalDirect {
		t.Fatalf("direct=%#v error=%v", direct, err)
	}
	_, err = resolveTransferOptions(
		context.Background(),
		&globalOptions{Transport: transportModeNative, NoDirect: true},
		appNegotiationTestToken,
		"receiver",
	)
	if err == nil || !strings.Contains(err.Error(), "requires direct TCP") {
		t.Fatalf("error = %v", err)
	}
	resolved, err := resolveTransferOptions(
		context.Background(),
		&globalOptions{Transport: transportModeNative, Local: true},
		appNegotiationTestToken,
		"receiver",
	)
	if err != nil || !resolved.Local {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	_, err = resolveTransferOptions(
		context.Background(),
		&globalOptions{Transport: transportModeNative, Proxy: "http://127.0.0.1:8080"},
		appNegotiationTestToken,
		"receiver",
	)
	if err == nil || !strings.Contains(err.Error(), "requires direct TCP") {
		t.Fatalf("proxy-only native error = %v", err)
	}
}

func TestNegotiationDisablesNativeDirectWithProxy(t *testing.T) {
	capabilityCh := make(chan negotiationCapability, 1)
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var capability negotiationCapability
		if err := conn.ReadJSON(&capability); err != nil {
			return
		}
		capabilityCh <- capability
		_ = conn.WriteJSON(negotiationResponse{
			Type:    "negotiated",
			Version: negotiationVersion,
			Route:   transportModeWebRTC,
			Reason:  "test",
		})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := negotiateTransferRoute(ctx, &globalOptions{
		Signal: server.URL,
		Relay:  "relay.example:9000",
		Proxy:  "socks5://proxy.example:1080",
	}, appNegotiationTestToken, "sender")
	if err != nil {
		t.Fatal(err)
	}
	capability := <-capabilityCh
	if capability.NativeDirect {
		t.Fatalf("capability = %#v", capability)
	}
}

func TestNegotiationURL(t *testing.T) {
	got, err := negotiationURL(
		"https://kigo.example/base?ignored=1#fragment",
		appNegotiationTestToken,
		"receiver",
	)
	if err != nil {
		t.Fatal(err)
	}
	want := "wss://kigo.example/base/api/negotiate/" + appNegotiationTestToken + "?role=receiver"
	if got != want {
		t.Fatalf("URL = %q, want %q", got, want)
	}
	got, err = negotiationURL("127.0.0.1:8080", appNegotiationTestToken, "sender")
	if err != nil {
		t.Fatal(err)
	}
	if got != "ws://127.0.0.1:8080/api/negotiate/"+appNegotiationTestToken+"?role=sender" {
		t.Fatalf("bare host URL = %q", got)
	}
}

func TestResolveTransferOptionsAppliesNegotiatedRelayWithoutMutatingOriginal(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/negotiate/"+appNegotiationTestToken ||
			r.URL.Query().Get("role") != "sender" {
			http.NotFound(w, r)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var capability negotiationCapability
		if err := conn.ReadJSON(&capability); err != nil {
			return
		}
		if capability.Client != "native" ||
			capability.NativeRelay != "client.example:9000" ||
			!capability.NativeLocal ||
			!capability.NativeDirect {
			_ = conn.WriteJSON(map[string]string{"type": "error", "error": "unexpected capability"})
			return
		}
		_ = conn.WriteJSON(negotiationResponse{
			Type:            "negotiated",
			Version:         negotiationVersion,
			Route:           transportModeNative,
			Relay:           "service.example:9000",
			RelayCredential: "temporary-room-credential",
			Direct:          true,
			Reason:          "service-native-relay",
		})
	}))
	defer server.Close()
	original := &globalOptions{
		Signal:    server.URL,
		Transport: transportModeAuto,
		Relay:     "client.example:9000",
		Local:     true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	resolved, err := resolveTransferOptions(ctx, original, appNegotiationTestToken, "sender")
	if err != nil {
		t.Fatal(err)
	}
	if resolved == original ||
		resolved.Relay != "service.example:9000" ||
		resolved.RelayPass != "temporary-room-credential" ||
		!resolved.SignalDirect ||
		resolved.Local {
		t.Fatalf("resolved = %#v", resolved)
	}
	if original.Relay != "client.example:9000" ||
		original.RelayPass != "" ||
		!original.Local {
		t.Fatalf("original options were mutated: %#v", original)
	}
}

func TestResolveTransferOptionsFallsBackForOldServer(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	nativeOriginal := &globalOptions{
		Signal:    server.URL,
		Transport: transportModeAuto,
		Relay:     "relay.example:9000",
	}
	nativeResolved, err := resolveTransferOptions(
		ctx,
		nativeOriginal,
		appNegotiationTestToken,
		"sender",
	)
	if err != nil {
		t.Fatal(err)
	}
	if nativeResolved == nativeOriginal || nativeResolved.Relay != nativeOriginal.Relay {
		t.Fatalf("native fallback = %#v", nativeResolved)
	}

	webRTCOriginal := &globalOptions{
		Signal:    server.URL,
		Transport: transportModeAuto,
		Local:     false,
	}
	webRTCResolved, err := resolveTransferOptions(
		ctx,
		webRTCOriginal,
		appNegotiationTestToken,
		"receiver",
	)
	if err != nil {
		t.Fatal(err)
	}
	if webRTCResolved == webRTCOriginal || webRTCResolved.Relay != "" || webRTCResolved.Local {
		t.Fatalf("WebRTC fallback = %#v", webRTCResolved)
	}
}

func TestResolveTransferOptionsFallsBackWhenServiceIsUnreachable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	resolved, err := resolveTransferOptions(
		ctx,
		&globalOptions{
			Signal:    "http://127.0.0.1:1",
			Transport: transportModeAuto,
		},
		appNegotiationTestToken,
		"sender",
	)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Relay != "" || resolved.Local {
		t.Fatalf("fallback = %#v", resolved)
	}
}

func TestResolveTransferOptionsDoesNotFallbackAfterContextDeadline(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var capability negotiationCapability
		if err := conn.ReadJSON(&capability); err != nil {
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	resolved, err := resolveTransferOptions(
		ctx,
		&globalOptions{Signal: server.URL, Transport: transportModeAuto},
		appNegotiationTestToken,
		"sender",
	)
	if resolved != nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("resolved=%#v error=%v", resolved, err)
	}
}
