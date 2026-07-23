package webrtcx

import (
	"crypto/tls"
	"net"
	"strings"
	"testing"
)

func TestNewPeerConnectionAcceptsInterfaceAndIPFilters(t *testing.T) {
	pc, err := newPeerConnection(withDefaults(Options{
		InterfaceFilter: func(name string) bool { return name == "test-interface" },
		IPFilter:        func(ip net.IP) bool { return ip.IsLoopback() },
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := pc.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSignalDialerClonesTLSConfig(t *testing.T) {
	config := &tls.Config{MinVersion: tls.VersionTLS13}
	dialer := signalDialer(Options{TLSClientConfig: config})
	if dialer.TLSClientConfig == nil || dialer.TLSClientConfig.MinVersion != tls.VersionTLS13 {
		t.Fatalf("TLS config = %#v", dialer.TLSClientConfig)
	}
	if dialer.TLSClientConfig == config {
		t.Fatal("signal dialer retained caller TLS config pointer")
	}
}

func TestReconnectState(t *testing.T) {
	var state ReconnectState
	if state.Supported() || state.Token() != "" || state.Generation() != 0 {
		t.Fatal("zero reconnect state is not empty")
	}
	state.update("secret-token", 7)
	if !state.Supported() {
		t.Fatal("updated reconnect state is not supported")
	}
	if state.Token() != "secret-token" {
		t.Fatalf("token = %q", state.Token())
	}
	if state.Generation() != 7 {
		t.Fatalf("generation = %d", state.Generation())
	}
	state.disable()
	if state.Supported() || state.Token() != "" || state.Generation() != 0 {
		t.Fatal("disabled reconnect state retained capability data")
	}
}

func TestSignalURLIncludesRoleButNotReconnectToken(t *testing.T) {
	got := signalURL("https://kigo.example/base", strings.Repeat("a", 64), "receiver")
	want := "wss://kigo.example/base/api/signal/" + strings.Repeat("a", 64) + "?role=receiver"
	if got != want {
		t.Fatalf("signal URL = %q, want %q", got, want)
	}
	if strings.Contains(got, "secret") || strings.Contains(got, "reconnect") {
		t.Fatalf("signal URL exposed reconnect data: %q", got)
	}
}

func TestSignalURLIncludesNoteProtocol(t *testing.T) {
	token := strings.Repeat("b", 64)
	got := signalURL("https://kigo.example/base", token, "sender", "note")
	want := "wss://kigo.example/base/api/signal/" + token + "?protocol=note&role=sender"
	if got != want {
		t.Fatalf("note signal URL = %q, want %q", got, want)
	}
}
