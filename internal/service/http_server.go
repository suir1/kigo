package service

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/suir1/kigo/internal/routing"
)

const (
	httpReadHeaderTimeout = 10 * time.Second
	httpWriteTimeout      = 30 * time.Second
	httpIdleTimeout       = 2 * time.Minute
	httpMaxHeaderBytes    = 64 << 10
)

const contentSecurityPolicy = "default-src 'self'; connect-src 'self' ws: wss:; img-src 'self' data:; style-src 'self'; script-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"

func (s *Server) Validate() error {
	return s.validateConfig()
}

func (s *Server) validateHTTPConfig() error {
	if s.cfg.SignalRequestsPerMinute < -1 {
		return errors.New("signaling request rate must be -1 or greater")
	}
	if s.cfg.NoteTTL <= 0 {
		return errors.New("persistent notepad TTL must be positive")
	}
	if _, err := parseTrustedProxies(s.cfg.TrustedProxies); err != nil {
		return err
	}
	if (s.cfg.TLSCert == "") != (s.cfg.TLSKey == "") {
		return errors.New("TLS certificate and key must be configured together")
	}
	publicURL, err := url.Parse(s.cfg.PublicURL)
	if err != nil ||
		(publicURL.Scheme != "http" && publicURL.Scheme != "https") ||
		publicURL.Host == "" ||
		publicURL.User != nil ||
		publicURL.RawQuery != "" ||
		publicURL.Fragment != "" ||
		(publicURL.Path != "" && publicURL.Path != "/") {
		return fmt.Errorf("public URL must be an http(s) origin: %q", s.cfg.PublicURL)
	}
	if s.usesTLS() && publicURL.Scheme != "https" {
		return errors.New("public URL must use https when direct TLS is enabled")
	}
	if s.usesTLS() {
		if _, err := tls.LoadX509KeyPair(s.cfg.TLSCert, s.cfg.TLSKey); err != nil {
			return fmt.Errorf("load TLS certificate and key: %w", err)
		}
	}
	if s.cfg.NativeRelay != "" {
		if err := routing.ValidateNativeRelay(s.cfg.NativeRelay); err != nil {
			return err
		}
	}
	if s.cfg.NativeRelaySecret != "" {
		if s.cfg.NativeRelay == "" {
			return errors.New("native relay secret requires a native relay endpoint")
		}
		if s.cfg.NativeRelayCredentialTTL <= 0 {
			return errors.New("native relay credential TTL must be positive")
		}
	}
	return nil
}

func (s *Server) newHTTPServer(ctx context.Context) *http.Server {
	return &http.Server{
		Addr:              s.cfg.Listen,
		Handler:           s.handler(),
		ReadHeaderTimeout: httpReadHeaderTimeout,
		WriteTimeout:      httpWriteTimeout,
		IdleTimeout:       httpIdleTimeout,
		MaxHeaderBytes:    httpMaxHeaderBytes,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
		},
		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}
}

func (s *Server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ice", s.handleICE)
	mux.HandleFunc("/api/health", s.handleHealth)
	mux.HandleFunc("/api/negotiate/", s.handleNegotiate)
	mux.HandleFunc("/api/direct/", s.handleDirect)
	mux.HandleFunc("/api/note-sync/", s.handlePersistentNote)
	mux.HandleFunc("/api/signal/", s.handleSignal)
	mux.HandleFunc("/favicon.ico", s.handleFavicon)
	mux.Handle("/", http.FileServer(s.webFileSystem()))
	return s.securityHeaders(mux)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	hsts := publicURLUsesHTTPS(s.cfg.PublicURL)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := w.Header()
		header.Set("Content-Security-Policy", contentSecurityPolicy)
		header.Set("Cross-Origin-Opener-Policy", "same-origin")
		header.Set("Cross-Origin-Resource-Policy", "same-origin")
		header.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("X-Frame-Options", "DENY")
		if hsts {
			header.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			header.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) checkOrigin(r *http.Request) bool {
	originValue := strings.TrimSpace(r.Header.Get("Origin"))
	if originValue == "" {
		return true
	}
	origin, err := url.Parse(originValue)
	if err != nil || origin.Scheme == "" || origin.Host == "" {
		return false
	}
	publicURL, err := url.Parse(s.cfg.PublicURL)
	if err == nil && sameOrigin(origin, publicURL) {
		return true
	}
	requestScheme := "http"
	if r.TLS != nil {
		requestScheme = "https"
	}
	return sameOrigin(origin, &url.URL{Scheme: requestScheme, Host: r.Host})
}

func sameOrigin(left, right *url.URL) bool {
	if left == nil || right == nil ||
		!strings.EqualFold(left.Scheme, right.Scheme) ||
		!strings.EqualFold(left.Hostname(), right.Hostname()) {
		return false
	}
	return originPort(left) == originPort(right)
}

func originPort(value *url.URL) string {
	if port := value.Port(); port != "" {
		return port
	}
	switch strings.ToLower(value.Scheme) {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

func defaultPublicURL(listen string, secure bool) string {
	scheme := "http"
	if secure {
		scheme = "https"
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return scheme + "://" + listen
	}
	host = strings.Trim(host, "[]")
	ip := net.ParseIP(host)
	if host == "" || ip != nil && ip.IsUnspecified() {
		host = "localhost"
	}
	return scheme + "://" + net.JoinHostPort(host, port)
}

func publicURLUsesHTTPS(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && strings.EqualFold(parsed.Scheme, "https")
}

func (s *Server) usesTLS() bool {
	return s.cfg.TLSCert != "" && s.cfg.TLSKey != ""
}
