package service

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseTrustedProxies(t *testing.T) {
	prefixes, err := parseTrustedProxies("127.0.0.1, 10.0.0.0/8, ::1")
	if err != nil {
		t.Fatal(err)
	}
	if len(prefixes) != 3 || prefixes[0].String() != "127.0.0.1/32" || prefixes[2].String() != "::1/128" {
		t.Fatalf("prefixes = %#v", prefixes)
	}
	if _, err := parseTrustedProxies("10.0.0.0/99"); err == nil {
		t.Fatal("invalid prefix was accepted")
	}
}

func TestClientAddressUsesOnlyTrustedForwardingChain(t *testing.T) {
	s := New(Config{TrustedProxies: "10.0.0.0/8"})
	tests := []struct {
		name      string
		remote    string
		forwarded string
		realIP    string
		want      string
	}{
		{
			name:      "trusted proxy chain",
			remote:    "10.0.0.2:8080",
			forwarded: "198.51.100.8, 10.0.0.3",
			want:      "198.51.100.8",
		},
		{
			name:      "direct spoof ignored",
			remote:    "198.51.100.9:8080",
			forwarded: "203.0.113.5",
			want:      "198.51.100.9:8080",
		},
		{
			name:      "malformed chain rejected",
			remote:    "10.0.0.2:8080",
			forwarded: "198.51.100.8, invalid",
			want:      "10.0.0.2",
		},
		{
			name:      "spoofed malformed prefix ignored after client",
			remote:    "10.0.0.2:8080",
			forwarded: "invalid, 198.51.100.11",
			want:      "198.51.100.11",
		},
		{
			name:   "real IP fallback",
			remote: "10.0.0.2:8080",
			realIP: "198.51.100.10",
			want:   "198.51.100.10",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			r.RemoteAddr = tt.remote
			r.Header.Set("X-Forwarded-For", tt.forwarded)
			r.Header.Set("X-Real-IP", tt.realIP)
			if got := s.clientAddress(r); got != tt.want {
				t.Fatalf("clientAddress() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRateLimitSeparatesClientsBehindTrustedProxy(t *testing.T) {
	s := New(Config{SignalRequestsPerMinute: 1, TrustedProxies: "10.0.0.2"})
	request := func(ip string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = "10.0.0.2:8080"
		r.Header.Set("X-Forwarded-For", ip)
		return r
	}
	if !s.allowRequest(request("198.51.100.1")) {
		t.Fatal("first client was unexpectedly limited")
	}
	if s.allowRequest(request("198.51.100.1")) {
		t.Fatal("first client exceeded its limit")
	}
	if !s.allowRequest(request("198.51.100.2")) {
		t.Fatal("second client shared the first client's limit")
	}
}
