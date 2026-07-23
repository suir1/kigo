package app

import (
	"strconv"

	"github.com/suir1/kigo/internal/netpolicy"
)

type clientSetting struct {
	key           string
	flag          string
	env           []string
	currentString func(*globalOptions) string
	savedString   func(userConfig) string
	applyString   func(*globalOptions, string)
	currentBool   func(*globalOptions) bool
	savedBool     func(userConfig) bool
	applyBool     func(*globalOptions, bool)
	savedSetter   func(*userConfig, string) error
	savedUnsetter func(*userConfig)
}

var clientSettingRegistry = []clientSetting{
	{
		key: "signal", flag: "signal", env: []string{"KIGO_SIGNAL", "KIGO_SIGNAL_URL"},
		currentString: func(g *globalOptions) string { return g.Signal },
		savedString:   func(c userConfig) string { return c.Signal },
		applyString:   func(g *globalOptions, value string) { g.Signal = value },
		savedSetter: func(c *userConfig, value string) error {
			normalized, err := normalizeClientHTTPURL("signal", value, false)
			if err == nil {
				c.Signal = normalized
			}
			return err
		},
		savedUnsetter: func(c *userConfig) { c.Signal = "" },
	},
	{
		key: "web-url", flag: "web-url", env: []string{"KIGO_WEB_URL"},
		currentString: func(g *globalOptions) string { return g.WebURL },
		savedString:   func(c userConfig) string { return c.WebURL },
		applyString:   func(g *globalOptions, value string) { g.WebURL = value },
		savedSetter: func(c *userConfig, value string) error {
			normalized, err := normalizeClientHTTPURL("web URL", value, false)
			if err == nil {
				c.WebURL = normalized
			}
			return err
		},
		savedUnsetter: func(c *userConfig) { c.WebURL = "" },
	},
	{
		key: "relay", flag: "relay", env: []string{"KIGO_RELAY"},
		currentString: func(g *globalOptions) string { return g.Relay },
		savedString:   func(c userConfig) string { return c.Relay },
		applyString:   func(g *globalOptions, value string) { g.Relay = value },
		savedSetter: func(c *userConfig, value string) error {
			if err := validateRelayEndpoint(value); err != nil {
				return err
			}
			c.Relay = value
			return nil
		},
		savedUnsetter: func(c *userConfig) { c.Relay = "" },
	},
	{
		key: "tls-ca", flag: "tls-ca", env: []string{"KIGO_TLS_CA"},
		currentString: func(g *globalOptions) string { return g.TLSCA },
		savedString:   func(c userConfig) string { return c.TLSCA },
		applyString:   func(g *globalOptions, value string) { g.TLSCA = value },
		savedSetter: func(c *userConfig, value string) error {
			normalized, _, err := loadTLSCABundle(value)
			if err == nil {
				c.TLSCA = normalized
			}
			return err
		},
		savedUnsetter: func(c *userConfig) { c.TLSCA = "" },
	},
	{
		key: "transport", flag: "transport", env: []string{"KIGO_TRANSPORT"},
		currentString: func(g *globalOptions) string { return g.Transport },
		savedString:   func(c userConfig) string { return c.Transport },
		applyString:   func(g *globalOptions, value string) { g.Transport = value },
		savedSetter: func(c *userConfig, value string) error {
			transport, err := normalizeTransportMode(value)
			if err == nil {
				c.Transport = transport
			}
			return err
		},
		savedUnsetter: func(c *userConfig) { c.Transport = "" },
	},
	{
		key: "interface", flag: "interface", env: []string{"KIGO_INTERFACE"},
		currentString: func(g *globalOptions) string { return g.Interface },
		savedString:   func(c userConfig) string { return c.Interface },
		applyString:   func(g *globalOptions, value string) { g.Interface = value },
		savedSetter: func(c *userConfig, value string) error {
			policy, err := netpolicy.Resolve(value)
			if err == nil {
				c.Interface = policy.InterfaceName()
			}
			return err
		},
		savedUnsetter: func(c *userConfig) { c.Interface = "" },
	},
	{
		key: "avoid-vpn", flag: "avoid-vpn", env: []string{"KIGO_AVOID_VPN"},
		currentBool: func(g *globalOptions) bool { return g.AvoidVPN },
		savedBool:   func(c userConfig) bool { return c.AvoidVPN },
		applyBool:   func(g *globalOptions, value bool) { g.AvoidVPN = value },
		savedSetter: func(c *userConfig, value string) error {
			parsed, err := parseNetworkConfigBool("avoid-vpn", value)
			if err == nil {
				c.AvoidVPN = parsed
			}
			return err
		},
		savedUnsetter: func(c *userConfig) { c.AvoidVPN = false },
	},
	{
		key: "no-auto-interface", flag: "no-auto-interface", env: []string{"KIGO_NO_AUTO_INTERFACE"},
		currentBool: func(g *globalOptions) bool { return g.NoAutoInterface },
		savedBool:   func(c userConfig) bool { return c.NoAutoInterface },
		applyBool:   func(g *globalOptions, value bool) { g.NoAutoInterface = value },
		savedSetter: func(c *userConfig, value string) error {
			parsed, err := parseNetworkConfigBool("no-auto-interface", value)
			if err == nil {
				c.NoAutoInterface = parsed
			}
			return err
		},
		savedUnsetter: func(c *userConfig) { c.NoAutoInterface = false },
	},
	{
		key: "proxy", flag: "proxy", env: []string{"KIGO_PROXY"},
		currentString: func(g *globalOptions) string { return g.Proxy },
		applyString:   func(g *globalOptions, value string) { g.Proxy = value },
	},
	{
		key: "discovery-addr", flag: "discovery-addr", env: []string{"KIGO_DISCOVERY_ADDR"},
		currentString: func(g *globalOptions) string { return g.DiscoveryAddr },
		applyString:   func(g *globalOptions, value string) { g.DiscoveryAddr = value },
	},
	{
		key: "relay-pass", flag: "relay-pass", env: []string{"KIGO_RELAY_PASS"},
		currentString: func(g *globalOptions) string { return g.RelayPass },
		applyString:   func(g *globalOptions, value string) { g.RelayPass = value },
	},
}

func findClientSetting(key string) *clientSetting {
	for index := range clientSettingRegistry {
		if clientSettingRegistry[index].key == key {
			return &clientSettingRegistry[index]
		}
	}
	return nil
}

func savedClientSettingValue(config userConfig, key string) string {
	setting := findClientSetting(key)
	if setting == nil {
		return ""
	}
	if setting.savedBool != nil {
		return strconv.FormatBool(setting.savedBool(config))
	}
	if setting.savedString != nil {
		return setting.savedString(config)
	}
	return ""
}
