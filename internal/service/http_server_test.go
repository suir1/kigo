package service

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestHTTPServerUsesProductionLimitsAndTLSFloor(t *testing.T) {
	s := New(Config{Listen: "127.0.0.1:8080"})
	server := s.newHTTPServer(context.Background())
	if server.ReadHeaderTimeout != httpReadHeaderTimeout ||
		server.WriteTimeout != httpWriteTimeout ||
		server.IdleTimeout != httpIdleTimeout ||
		server.MaxHeaderBytes != httpMaxHeaderBytes {
		t.Fatalf("HTTP server limits = %#v", server)
	}
	if server.TLSConfig == nil || server.TLSConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("TLS config = %#v", server.TLSConfig)
	}
}

func TestSecurityHeadersCoverAssetsAndDisableAPICaching(t *testing.T) {
	s := New(Config{
		Listen:    "127.0.0.1:443",
		PublicURL: "https://kigo.example",
	})
	root := httptest.NewRecorder()
	s.handler().ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/", nil))
	for name, want := range map[string]string{
		"Content-Security-Policy":      contentSecurityPolicy,
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Permissions-Policy":           "camera=(), microphone=(), geolocation=()",
		"Referrer-Policy":              "no-referrer",
		"Strict-Transport-Security":    "max-age=31536000",
		"X-Content-Type-Options":       "nosniff",
		"X-Frame-Options":              "DENY",
	} {
		if got := root.Header().Get(name); got != want {
			t.Fatalf("%s = %q, want %q", name, got, want)
		}
	}
	if got := root.Header().Get("Cache-Control"); got != "" {
		t.Fatalf("asset cache control = %q", got)
	}

	api := httptest.NewRecorder()
	s.handler().ServeHTTP(api, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if got := api.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("API cache control = %q", got)
	}
}

func TestCheckOriginAllowsNativeSameOriginAndConfiguredOrigin(t *testing.T) {
	s := New(Config{PublicURL: "https://kigo.example"})
	tests := []struct {
		name   string
		host   string
		origin string
		tls    bool
		want   bool
	}{
		{name: "native", host: "kigo.example", want: true},
		{name: "request host", host: "127.0.0.1:8080", origin: "http://127.0.0.1:8080", want: true},
		{name: "configured origin", host: "internal:8080", origin: "https://kigo.example", want: true},
		{name: "default https port", host: "internal:8080", origin: "https://kigo.example:443", want: true},
		{name: "wrong scheme", host: "kigo.example", origin: "http://kigo.example", tls: true, want: false},
		{name: "cross site", host: "kigo.example", origin: "https://evil.example", tls: true, want: false},
		{name: "malformed", host: "kigo.example", origin: "null", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/signal/token", nil)
			request.Host = tt.host
			if tt.origin != "" {
				request.Header.Set("Origin", tt.origin)
			}
			if tt.tls {
				request.TLS = &tls.ConnectionState{}
			}
			if got := s.checkOrigin(request); got != tt.want {
				t.Fatalf("checkOrigin() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestSignalWebSocketEnforcesBrowserOrigin(t *testing.T) {
	s := New(Config{PublicURL: "http://kigo.example"})
	server := httptest.NewServer(s.handler())
	defer server.Close()
	wsURL := "ws" + server.URL[len("http"):] + "/api/signal/" + strings.Repeat("a", 64)

	sameOriginHeader := http.Header{"Origin": []string{server.URL}}
	conn, response, err := websocket.DefaultDialer.Dial(wsURL, sameOriginHeader)
	if err != nil {
		t.Fatalf("same-origin dial failed: %v", err)
	}
	if response.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("same-origin status = %d", response.StatusCode)
	}
	_ = conn.Close()

	crossOriginHeader := http.Header{"Origin": []string{"https://evil.example"}}
	conn, response, err = websocket.DefaultDialer.Dial(wsURL, crossOriginHeader)
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("cross-origin WebSocket was accepted")
	}
	if response == nil || response.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin response = %#v, err = %v", response, err)
	}
}

func TestValidateHTTPConfigAndDefaultPublicURL(t *testing.T) {
	if got := New(Config{}).cfg.PublicURL; got != "http://localhost:9100" {
		t.Fatalf("default public URL = %q", got)
	}
	if got := New(Config{Listen: ":443", TLSCert: "cert.pem", TLSKey: "key.pem"}).cfg.PublicURL; got != "https://localhost:443" {
		t.Fatalf("default TLS public URL = %q", got)
	}
	tests := []struct {
		name string
		cfg  Config
		want string
	}{
		{
			name: "certificate without key",
			cfg:  Config{TLSCert: "cert.pem"},
			want: "configured together",
		},
		{
			name: "native relay secret without endpoint",
			cfg: Config{
				NativeRelaySecret: "secret",
			},
			want: "requires a native relay endpoint",
		},
		{
			name: "invalid native relay credential TTL",
			cfg: Config{
				NativeRelay:              "relay.example:9000",
				NativeRelaySecret:        "secret",
				NativeRelayCredentialTTL: -time.Second,
			},
			want: "credential TTL must be positive",
		},
		{
			name: "invalid public URL",
			cfg:  Config{PublicURL: "kigo.example"},
			want: "http(s) origin",
		},
		{
			name: "public URL path",
			cfg:  Config{PublicURL: "https://kigo.example/base"},
			want: "http(s) origin",
		},
		{
			name: "invalid trusted proxy",
			cfg:  Config{TrustedProxies: "10.0.0.0/99"},
			want: "invalid trusted proxy",
		},
		{
			name: "direct TLS with HTTP public URL",
			cfg: Config{
				PublicURL: "http://kigo.example",
				TLSCert:   "cert.pem",
				TLSKey:    "key.pem",
			},
			want: "must use https",
		},
		{
			name: "missing TLS files",
			cfg: Config{
				PublicURL: "https://kigo.example",
				TLSCert:   "missing-cert.pem",
				TLSKey:    "missing-key.pem",
			},
			want: "load TLS certificate",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := New(tt.cfg).Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want %q", err, tt.want)
			}
		})
	}
}
