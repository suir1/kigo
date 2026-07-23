package app

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

var (
	defaultSignalURL = "http://127.0.0.1:9100"
	defaultWebURL    = "http://127.0.0.1:9100"
)

func clientRuntimeCommand(cmd *cobra.Command) bool {
	if cmd == nil {
		return false
	}
	top := cmd
	for top.Parent() != nil && top.Parent().Parent() != nil {
		top = top.Parent()
	}
	switch top.Name() {
	case "send", "recv", "text", "note", "doctor", "route", "tui", "web":
		return true
	default:
		return false
	}
}

func applyClientRuntimeConfig(cmd *cobra.Command, g *globalOptions) error {
	if g == nil {
		return nil
	}
	config, err := loadUserConfig()
	if err != nil {
		fmt.Fprintln(cmd.ErrOrStderr(), "warning: could not load client config:", err)
		config = defaultUserConfig()
	}
	for _, setting := range clientSettingRegistry {
		if setting.currentBool != nil {
			saved := false
			if setting.savedBool != nil {
				saved = setting.savedBool(config)
			}
			value, boolErr := runtimeBool(cmd, setting.flag, setting.currentBool(g), saved, setting.env[0])
			if boolErr != nil {
				return boolErr
			}
			setting.applyBool(g, value)
			continue
		}
		saved := ""
		if setting.savedString != nil {
			saved = setting.savedString(config)
		}
		setting.applyString(g, runtimeString(cmd, setting.flag, setting.currentString(g), saved, setting.env...))
	}

	g.Signal, err = normalizeClientHTTPURL("signal", g.Signal, false)
	if err != nil {
		return err
	}
	g.WebURL, err = normalizeClientHTTPURL("web URL", g.WebURL, true)
	if err != nil {
		return err
	}
	g.Transport, err = normalizeTransportMode(g.Transport)
	if err != nil {
		return err
	}
	if err := validateRelayEndpoint(g.Relay); err != nil {
		return err
	}
	return nil
}

func runtimeBool(
	cmd *cobra.Command,
	flagName string,
	current bool,
	saved bool,
	envName string,
) (bool, error) {
	if commandFlagChanged(cmd, flagName) {
		return current, nil
	}
	if value := strings.TrimSpace(os.Getenv(envName)); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return false, fmt.Errorf("%s: expected true or false: %w", envName, err)
		}
		return parsed, nil
	}
	return saved, nil
}

func runtimeString(
	cmd *cobra.Command,
	flagName string,
	current string,
	saved string,
	envNames ...string,
) string {
	if commandFlagChanged(cmd, flagName) {
		return strings.TrimSpace(current)
	}
	for _, name := range envNames {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	if saved = strings.TrimSpace(saved); saved != "" {
		return saved
	}
	return strings.TrimSpace(current)
}

func commandFlagChanged(cmd *cobra.Command, name string) bool {
	if cmd == nil {
		return false
	}
	if flag := cmd.Flags().Lookup(name); flag != nil && flag.Changed {
		return true
	}
	if flag := cmd.InheritedFlags().Lookup(name); flag != nil && flag.Changed {
		return true
	}
	return false
}

func normalizeClientHTTPURL(label, value string, allowEmpty bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if allowEmpty {
			return "", nil
		}
		return "", fmt.Errorf("%s URL is empty", label)
	}
	if !strings.Contains(value, "://") {
		value = "http://" + value
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return "", fmt.Errorf("invalid %s URL %q; use http:// or https://", label, value)
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("invalid %s URL %q; credentials, query, and fragment are not supported", label, value)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}

func validateRelayEndpoint(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	host, portText, err := net.SplitHostPort(value)
	if err != nil || strings.TrimSpace(host) == "" {
		return fmt.Errorf("invalid relay endpoint %q; use host:port", value)
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("invalid relay endpoint %q; port must be between 1 and 65535", value)
	}
	return nil
}
