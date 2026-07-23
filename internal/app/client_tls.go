package app

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxTLSCABundleBytes = 8 << 20

func configureClientTLS(g *globalOptions) error {
	if g == nil || strings.TrimSpace(g.TLSCA) == "" {
		return nil
	}
	normalizedPath, pemData, err := loadTLSCABundle(g.TLSCA)
	if err != nil {
		return err
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(pemData) {
		return errors.New("TLS CA bundle does not contain a valid PEM certificate")
	}
	g.TLSCA = normalizedPath
	g.tlsConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    roots,
	}
	return nil
}

func loadTLSCABundle(path string) (string, []byte, error) {
	normalizedPath, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", nil, fmt.Errorf("TLS CA bundle path: %w", err)
	}
	info, err := os.Stat(normalizedPath)
	if err != nil {
		return "", nil, fmt.Errorf("TLS CA bundle: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxTLSCABundleBytes {
		return "", nil, errors.New("TLS CA bundle must be a regular PEM file no larger than 8 MiB")
	}
	pemData, err := os.ReadFile(normalizedPath)
	if err != nil {
		return "", nil, fmt.Errorf("read TLS CA bundle: %w", err)
	}
	validationPool := x509.NewCertPool()
	if !validationPool.AppendCertsFromPEM(pemData) {
		return "", nil, errors.New("TLS CA bundle does not contain a valid PEM certificate")
	}
	return normalizedPath, pemData, nil
}

func clientTLSConfig(g *globalOptions) *tls.Config {
	if g == nil || g.tlsConfig == nil {
		return nil
	}
	return g.tlsConfig.Clone()
}
