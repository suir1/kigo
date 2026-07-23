package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

type networkConfigView struct {
	Path            string `json:"path"`
	Signal          string `json:"signal,omitempty"`
	WebURL          string `json:"web_url,omitempty"`
	Relay           string `json:"relay,omitempty"`
	TLSCA           string `json:"tls_ca,omitempty"`
	Transport       string `json:"transport,omitempty"`
	Interface       string `json:"interface,omitempty"`
	AvoidVPN        bool   `json:"avoid_vpn"`
	NoAutoInterface bool   `json:"no_auto_interface"`
}

func newConfigCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "config",
		Short: "Manage persistent client network configuration",
	}
	var jsonOutput bool
	show := &cobra.Command{
		Use:   "show",
		Short: "Show saved non-secret client configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			config, err := loadUserConfig()
			if err != nil {
				return err
			}
			view := networkConfigView{
				Path:            userConfigPath(),
				Signal:          savedClientSettingValue(config, "signal"),
				WebURL:          savedClientSettingValue(config, "web-url"),
				Relay:           savedClientSettingValue(config, "relay"),
				TLSCA:           savedClientSettingValue(config, "tls-ca"),
				Transport:       savedClientSettingValue(config, "transport"),
				Interface:       savedClientSettingValue(config, "interface"),
				AvoidVPN:        savedClientSettingValue(config, "avoid-vpn") == "true",
				NoAutoInterface: savedClientSettingValue(config, "no-auto-interface") == "true",
			}
			if jsonOutput {
				encoder := json.NewEncoder(cmd.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(view)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Config:", view.Path)
			printSavedConfigValue(cmd, "signal", view.Signal)
			printSavedConfigValue(cmd, "web-url", view.WebURL)
			printSavedConfigValue(cmd, "relay", view.Relay)
			printSavedConfigValue(cmd, "tls-ca", view.TLSCA)
			printSavedConfigValue(cmd, "transport", view.Transport)
			printSavedConfigValue(cmd, "interface", view.Interface)
			printSavedConfigValue(cmd, "avoid-vpn", strconv.FormatBool(view.AvoidVPN))
			printSavedConfigValue(cmd, "no-auto-interface", strconv.FormatBool(view.NoAutoInterface))
			return nil
		},
	}
	show.Flags().BoolVar(&jsonOutput, "json", false, "write machine-readable configuration")

	set := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Save one non-secret client setting",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			config, err := loadUserConfig()
			if err != nil {
				return err
			}
			key, value := normalizeConfigKey(args[0]), strings.TrimSpace(args[1])
			if err := setNetworkConfigValue(&config, key, value); err != nil {
				return err
			}
			if err := saveUserConfig(config); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Saved %s in %s\n", key, userConfigPath())
			return nil
		},
	}

	unset := &cobra.Command{
		Use:   "unset <key>",
		Short: "Remove one saved client setting",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			config, err := loadUserConfig()
			if err != nil {
				return err
			}
			key := normalizeConfigKey(args[0])
			if err := unsetNetworkConfigValue(&config, key); err != nil {
				return err
			}
			if err := saveUserConfig(config); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Unset %s in %s\n", key, userConfigPath())
			return nil
		},
	}

	path := &cobra.Command{
		Use:   "path",
		Short: "Print the client configuration file path",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), userConfigPath())
		},
	}
	command.AddCommand(show, set, unset, path)
	return command
}

func printSavedConfigValue(cmd *cobra.Command, key, value string) {
	if value == "" {
		value = "(not set)"
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", key, value)
}

func normalizeConfigKey(key string) string {
	return strings.ToLower(strings.TrimSpace(strings.ReplaceAll(key, "_", "-")))
}

func setNetworkConfigValue(config *userConfig, key, value string) error {
	if config == nil {
		return errors.New("client config is unavailable")
	}
	if value == "" {
		return errors.New("config value is empty; use config unset to remove a setting")
	}
	if key == "service" {
		normalized, err := normalizeClientHTTPURL("service", value, false)
		if err != nil {
			return err
		}
		config.Signal, config.WebURL = normalized, normalized
		return nil
	}
	setting := findClientSetting(key)
	if setting == nil || setting.savedSetter == nil {
		return unknownNetworkConfigKey(key)
	}
	return setting.savedSetter(config, value)
}

func unsetNetworkConfigValue(config *userConfig, key string) error {
	if config == nil {
		return errors.New("client config is unavailable")
	}
	if key == "service" {
		config.Signal = ""
		config.WebURL = ""
		return nil
	}
	setting := findClientSetting(key)
	if setting == nil || setting.savedUnsetter == nil {
		return unknownNetworkConfigKey(key)
	}
	setting.savedUnsetter(config)
	return nil
}

func unknownNetworkConfigKey(key string) error {
	return fmt.Errorf("unknown config key %q; use service, signal, web-url, relay, tls-ca, transport, interface, avoid-vpn, or no-auto-interface", key)
}

func parseNetworkConfigBool(key, value string) (bool, error) {
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("config %s must be true or false", key)
	}
	return parsed, nil
}
