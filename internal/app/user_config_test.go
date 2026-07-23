package app

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUserConfigRoundTripAndPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.json")
	t.Setenv("KIGO_CONFIG_PATH", path)

	initial, err := loadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if initial.Version != userConfigVersion ||
		initial.LastMode != "send" ||
		initial.LastReceiveDir != "." ||
		initial.SymlinkMode != "follow" {
		t.Fatalf("initial config = %#v", initial)
	}

	want := userConfig{
		Version:           userConfigVersion,
		LastMode:          "receive",
		LastSendPath:      "/tmp/send",
		LastReceiveDir:    "/tmp/receive",
		SymlinkMode:       "preserve",
		IncludeGitIgnored: true,
		ConflictPolicy:    "rename",
		DoctorTimeout:     "5s",
		AvoidVPN:          true,
		NoAutoInterface:   true,
		TLSCA:             "/tmp/ca.pem",
	}
	if err := saveUserConfig(want); err != nil {
		t.Fatal(err)
	}
	got, err := loadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("config = %#v, want %#v", got, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("config mode = %o", info.Mode().Perm())
		}
	}
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".config-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary config files remain: %#v", matches)
	}
}

func TestUserConfigSanitizesUnknownValues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("KIGO_CONFIG_PATH", path)
	if err := os.WriteFile(path, []byte(`{
  "version": 1,
  "last_mode": "other",
  "last_receive_dir": "",
  "symlink_mode": "copy",
  "conflict_policy": "replace",
  "doctor_timeout": "forever"
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.LastMode != "send" ||
		got.LastReceiveDir != "." ||
		got.SymlinkMode != "follow" ||
		got.ConflictPolicy != "overwrite" ||
		got.DoctorTimeout != "3s" {
		t.Fatalf("sanitized config = %#v", got)
	}
}

func TestUserConfigAcceptsNotepadMode(t *testing.T) {
	config := defaultUserConfig()
	config.LastMode = "note"
	sanitizeUserConfig(&config)
	if config.LastMode != "note" {
		t.Fatalf("last mode = %q", config.LastMode)
	}
}

func TestUserConfigReportsMalformedAndNewerFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("KIGO_CONFIG_PATH", path)
	if err := os.WriteFile(path, []byte(`{"version":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadUserConfig(); err == nil || !strings.Contains(err.Error(), "read user config") {
		t.Fatalf("malformed config error = %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadUserConfig(); err == nil || !strings.Contains(err.Error(), "newer") {
		t.Fatalf("newer config error = %v", err)
	}
}

func TestRememberSettingsPreservesOtherFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("KIGO_CONFIG_PATH", path)
	initial := defaultUserConfig()
	initial.Signal = "https://kigo.example"
	initial.WebURL = "https://web.kigo.example"
	initial.Relay = "relay.kigo.example:9000"
	initial.TLSCA = "/tmp/ca.pem"
	initial.Transport = transportModeNative
	initial.AvoidVPN = true
	initial.NoAutoInterface = true
	if err := saveUserConfig(initial); err != nil {
		t.Fatal(err)
	}
	if err := rememberSendSettings("/tmp/send", "preserve", true); err != nil {
		t.Fatal(err)
	}
	if err := rememberReceiveSettings("/tmp/out", "skip"); err != nil {
		t.Fatal(err)
	}
	got, err := loadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if got.LastMode != "receive" ||
		got.LastSendPath != "/tmp/send" ||
		got.LastReceiveDir != "/tmp/out" ||
		got.SymlinkMode != "preserve" ||
		!got.IncludeGitIgnored ||
		got.ConflictPolicy != "skip" {
		t.Fatalf("remembered config = %#v", got)
	}
	if got.Signal != initial.Signal || got.WebURL != initial.WebURL || got.Relay != initial.Relay || got.TLSCA != initial.TLSCA ||
		got.Transport != initial.Transport || got.AvoidVPN != initial.AvoidVPN ||
		got.NoAutoInterface != initial.NoAutoInterface {
		t.Fatalf("remembering UI settings replaced network config: %#v", got)
	}
}
