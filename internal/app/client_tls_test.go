package app

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestConfigureClientTLSLoadsAdditionalCA(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	certificate := server.Certificate()
	caPath := t.TempDir() + "/ca.pem"
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	g := &globalOptions{TLSCA: caPath}
	if err := configureClientTLS(g); err != nil {
		t.Fatal(err)
	}
	client := outboundHTTPClient(g)
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
}

func TestConfigureClientTLSRejectsInvalidBundle(t *testing.T) {
	path := t.TempDir() + "/ca.pem"
	if err := os.WriteFile(path, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := configureClientTLS(&globalOptions{TLSCA: path}); err == nil {
		t.Fatal("invalid CA bundle was accepted")
	}
}

func newTestTLSCAPath(t *testing.T) string {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	certificate := server.Certificate()
	server.Close()
	caPath := t.TempDir() + "/ca.pem"
	if err := os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return caPath
}
