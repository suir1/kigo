package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/suir1/kigo/internal/localstate"
)

const userConfigVersion = 1
const userConfigMaxBytes = 1 << 20

type userConfig struct {
	Version           int    `json:"version"`
	Signal            string `json:"signal,omitempty"`
	WebURL            string `json:"web_url,omitempty"`
	Relay             string `json:"relay,omitempty"`
	TLSCA             string `json:"tls_ca,omitempty"`
	Transport         string `json:"transport,omitempty"`
	Interface         string `json:"interface,omitempty"`
	AvoidVPN          bool   `json:"avoid_vpn,omitempty"`
	NoAutoInterface   bool   `json:"no_auto_interface,omitempty"`
	LastMode          string `json:"last_mode,omitempty"`
	LastSendPath      string `json:"last_send_path,omitempty"`
	LastReceiveDir    string `json:"last_receive_dir,omitempty"`
	SymlinkMode       string `json:"symlink_mode,omitempty"`
	IncludeGitIgnored bool   `json:"include_git_ignored,omitempty"`
	ConflictPolicy    string `json:"conflict_policy,omitempty"`
	DoctorTimeout     string `json:"doctor_timeout,omitempty"`
}

func defaultUserConfig() userConfig {
	return userConfig{
		Version:        userConfigVersion,
		LastMode:       "send",
		LastReceiveDir: ".",
		SymlinkMode:    "follow",
		ConflictPolicy: "overwrite",
		DoctorTimeout:  "3s",
	}
}

func userConfigPath() string {
	if path := strings.TrimSpace(os.Getenv("KIGO_CONFIG_PATH")); path != "" {
		return filepath.Clean(path)
	}
	dir, err := os.UserConfigDir()
	if err != nil || strings.TrimSpace(dir) == "" {
		return filepath.Clean(".kigo-config.json")
	}
	return filepath.Join(dir, "kigo", "config.json")
}

func loadUserConfig() (userConfig, error) {
	config := defaultUserConfig()
	path := userConfigPath()
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return config, nil
	}
	if err != nil {
		return config, err
	}
	defer file.Close()

	decoder := json.NewDecoder(io.LimitReader(file, userConfigMaxBytes+1))
	var loaded userConfig
	if err := decoder.Decode(&loaded); err != nil {
		return config, fmt.Errorf("read user config: %w", err)
	}
	if loaded.Version > userConfigVersion {
		return config, fmt.Errorf("user config version %d is newer than supported version %d", loaded.Version, userConfigVersion)
	}
	sanitizeUserConfig(&loaded)
	return loaded, nil
}

func sanitizeUserConfig(config *userConfig) {
	if config == nil {
		return
	}
	config.Version = userConfigVersion
	config.Signal = strings.TrimSpace(config.Signal)
	config.WebURL = strings.TrimSpace(config.WebURL)
	config.Relay = strings.TrimSpace(config.Relay)
	config.TLSCA = strings.TrimSpace(config.TLSCA)
	config.Interface = strings.TrimSpace(config.Interface)
	if config.Transport != "" {
		transport, err := normalizeTransportMode(config.Transport)
		if err != nil {
			config.Transport = ""
		} else {
			config.Transport = transport
		}
	}
	switch config.LastMode {
	case "send", "receive", "doctor", "note":
	default:
		config.LastMode = "send"
	}
	switch config.SymlinkMode {
	case "follow", "preserve":
	default:
		config.SymlinkMode = "follow"
	}
	switch config.ConflictPolicy {
	case "overwrite", "rename", "skip":
	default:
		config.ConflictPolicy = "overwrite"
	}
	switch config.DoctorTimeout {
	case "2s", "3s", "5s", "10s":
	default:
		config.DoctorTimeout = "3s"
	}
	if strings.TrimSpace(config.LastReceiveDir) == "" {
		config.LastReceiveDir = "."
	}
}

func saveUserConfig(config userConfig) error {
	sanitizeUserConfig(&config)
	if err := localstate.WriteJSON(userConfigPath(), config); err != nil {
		return fmt.Errorf("save user config: %w", err)
	}
	return nil
}

func rememberSendSettings(path, symlinks string, includeGitIgnored bool) error {
	config, err := loadUserConfig()
	if err != nil {
		config = defaultUserConfig()
	}
	config.LastMode = "send"
	config.LastSendPath = path
	config.SymlinkMode = symlinks
	config.IncludeGitIgnored = includeGitIgnored
	return saveUserConfig(config)
}

func rememberReceiveSettings(outputDir, conflict string) error {
	config, err := loadUserConfig()
	if err != nil {
		config = defaultUserConfig()
	}
	config.LastMode = "receive"
	config.LastReceiveDir = outputDir
	config.ConflictPolicy = conflict
	return saveUserConfig(config)
}
