package app

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/suir1/kigo/internal/discovery"
)

func TestClientRuntimeConfigPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("KIGO_CONFIG_PATH", path)
	for _, name := range []string{
		"KIGO_SIGNAL", "KIGO_SIGNAL_URL", "KIGO_WEB_URL", "KIGO_RELAY",
		"KIGO_TRANSPORT", "KIGO_INTERFACE", "KIGO_PROXY", "KIGO_DISCOVERY_ADDR",
		"KIGO_RELAY_PASS", "KIGO_TLS_CA", "KIGO_AVOID_VPN", "KIGO_NO_AUTO_INTERFACE",
	} {
		t.Setenv(name, "")
	}
	config := defaultUserConfig()
	config.Signal = "https://saved.example/"
	config.WebURL = "https://saved-web.example/"
	config.Relay = "saved.example:9000"
	config.TLSCA = filepath.Join(t.TempDir(), "saved-ca.pem")
	config.Transport = transportModeNative
	config.AvoidVPN = true
	if err := saveUserConfig(config); err != nil {
		t.Fatal(err)
	}

	g, command := runtimeConfigTestCommand()
	if err := applyClientRuntimeConfig(command, g); err != nil {
		t.Fatal(err)
	}
	if g.Signal != "https://saved.example" || g.WebURL != "https://saved-web.example" ||
		g.Relay != "saved.example:9000" || g.TLSCA != config.TLSCA || g.Transport != transportModeNative || !g.AvoidVPN {
		t.Fatalf("saved runtime config = %#v", g)
	}

	t.Setenv("KIGO_SIGNAL", "https://env.example/")
	t.Setenv("KIGO_RELAY", "env.example:9001")
	envCA := filepath.Join(t.TempDir(), "env-ca.pem")
	t.Setenv("KIGO_TLS_CA", envCA)
	t.Setenv("KIGO_TRANSPORT", transportModeAuto)
	t.Setenv("KIGO_AVOID_VPN", "false")
	t.Setenv("KIGO_NO_AUTO_INTERFACE", "true")
	g, command = runtimeConfigTestCommand()
	if err := applyClientRuntimeConfig(command, g); err != nil {
		t.Fatal(err)
	}
	if g.Signal != "https://env.example" || g.Relay != "env.example:9001" || g.TLSCA != envCA || g.Transport != transportModeAuto ||
		g.AvoidVPN || !g.NoAutoInterface {
		t.Fatalf("environment runtime config = %#v", g)
	}

	g, command = runtimeConfigTestCommand()
	if err := command.Flags().Set("signal", "https://flag.example/"); err != nil {
		t.Fatal(err)
	}
	if err := command.Flags().Set("relay", "flag.example:9002"); err != nil {
		t.Fatal(err)
	}
	flagCA := filepath.Join(t.TempDir(), "flag-ca.pem")
	if err := command.Flags().Set("tls-ca", flagCA); err != nil {
		t.Fatal(err)
	}
	if err := command.Flags().Set("avoid-vpn", "true"); err != nil {
		t.Fatal(err)
	}
	if err := applyClientRuntimeConfig(command, g); err != nil {
		t.Fatal(err)
	}
	if g.Signal != "https://flag.example" || g.Relay != "flag.example:9002" || g.TLSCA != flagCA || !g.AvoidVPN {
		t.Fatalf("flag runtime config = %#v", g)
	}
}

func TestConfigCommandSetShowUnsetAndRejectsSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("KIGO_CONFIG_PATH", path)
	caPath := newTestTLSCAPath(t)
	for _, args := range [][]string{
		{"config", "set", "service", "https://kigo.example/"},
		{"config", "set", "web_url", "https://web.kigo.example/"},
		{"config", "set", "relay", "relay.kigo.example:9000"},
		{"config", "set", "tls-ca", caPath},
		{"config", "set", "transport", "native"},
		{"config", "set", "avoid_vpn", "true"},
		{"config", "set", "no-auto-interface", "true"},
	} {
		if _, err := executeConfigCommand(args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	output, err := executeConfigCommand("config", "show", "--json")
	if err != nil {
		t.Fatal(err)
	}
	var view networkConfigView
	if err := json.Unmarshal(output.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.Path != path || view.Signal != "https://kigo.example" ||
		view.WebURL != "https://web.kigo.example" || view.Relay != "relay.kigo.example:9000" ||
		view.TLSCA != caPath || view.Transport != transportModeNative || !view.AvoidVPN || !view.NoAutoInterface {
		t.Fatalf("config view = %#v", view)
	}
	if _, err := executeConfigCommand("config", "set", "relay-pass", "secret"); err == nil ||
		!strings.Contains(err.Error(), "unknown config key") {
		t.Fatalf("secret config error = %v", err)
	}
	if _, err := executeConfigCommand("config", "set", "avoid-vpn", "sometimes"); err == nil ||
		!strings.Contains(err.Error(), "true or false") {
		t.Fatalf("boolean config error = %v", err)
	}
	if _, err := executeConfigCommand("config", "unset", "relay"); err != nil {
		t.Fatal(err)
	}
	if _, err := executeConfigCommand("config", "unset", "tls-ca"); err != nil {
		t.Fatal(err)
	}
	if _, err := executeConfigCommand("config", "unset", "avoid-vpn"); err != nil {
		t.Fatal(err)
	}
	config, err := loadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.Relay != "" || config.TLSCA != "" || config.Signal == "" || config.AvoidVPN {
		t.Fatalf("config after unset = %#v", config)
	}
}

func TestClientRuntimeConfigRejectsInvalidSavedEndpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("KIGO_CONFIG_PATH", path)
	config := defaultUserConfig()
	config.Relay = "missing-port"
	if err := saveUserConfig(config); err != nil {
		t.Fatal(err)
	}
	g, command := runtimeConfigTestCommand()
	err := applyClientRuntimeConfig(command, g)
	if err == nil || !strings.Contains(err.Error(), "invalid relay endpoint") {
		t.Fatalf("runtime config error = %v", err)
	}
}

func TestClientRuntimeConfigRejectsInvalidBooleanEnvironment(t *testing.T) {
	t.Setenv("KIGO_CONFIG_PATH", filepath.Join(t.TempDir(), "config.json"))
	t.Setenv("KIGO_AVOID_VPN", "sometimes")
	g, command := runtimeConfigTestCommand()
	err := applyClientRuntimeConfig(command, g)
	if err == nil || !strings.Contains(err.Error(), "KIGO_AVOID_VPN") {
		t.Fatalf("runtime boolean error = %v", err)
	}
}

func runtimeConfigTestCommand() (*globalOptions, *cobra.Command) {
	g := &globalOptions{
		Signal:        defaultSignalURL,
		WebURL:        defaultWebURL,
		Transport:     transportModeAuto,
		DiscoveryAddr: discovery.DefaultAddr,
	}
	command := &cobra.Command{Use: "test"}
	command.Flags().StringVar(&g.Signal, "signal", g.Signal, "")
	command.Flags().StringVar(&g.WebURL, "web-url", g.WebURL, "")
	command.Flags().StringVar(&g.Relay, "relay", "", "")
	command.Flags().StringVar(&g.RelayPass, "relay-pass", "", "")
	command.Flags().StringVar(&g.Proxy, "proxy", "", "")
	command.Flags().StringVar(&g.TLSCA, "tls-ca", "", "")
	command.Flags().StringVar(&g.Interface, "interface", "", "")
	command.Flags().BoolVar(&g.AvoidVPN, "avoid-vpn", false, "")
	command.Flags().BoolVar(&g.NoAutoInterface, "no-auto-interface", false, "")
	command.Flags().StringVar(&g.Transport, "transport", g.Transport, "")
	command.Flags().StringVar(&g.DiscoveryAddr, "discovery-addr", g.DiscoveryAddr, "")
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	return g, command
}

func executeConfigCommand(args ...string) (*bytes.Buffer, error) {
	command := NewRootCommand()
	output := &bytes.Buffer{}
	command.SetOut(output)
	command.SetErr(output)
	command.SetArgs(args)
	return output, command.Execute()
}
