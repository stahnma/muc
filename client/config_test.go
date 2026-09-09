package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadClientConfig_Defaults(t *testing.T) {
	cfg := loadClientConfigFromPaths([]string{t.TempDir()})
	if cfg.NATSURL != "" {
		t.Errorf("NATSURL = %q, want empty string", cfg.NATSURL)
	}
	if cfg.NATSPort != "4222" {
		t.Errorf("NATSPort = %q, want %q", cfg.NATSPort, "4222")
	}
}

func TestLoadClientConfig_FromFile(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "client.yml"), []byte("nats_url: nats://test:4222\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cfg := loadClientConfigFromPaths([]string{dir})
	if cfg.NATSURL != "nats://test:4222" {
		t.Errorf("NATSURL = %q, want %q", cfg.NATSURL, "nats://test:4222")
	}
}

func TestLoadClientConfig_EnvOverridesFile(t *testing.T) {
	dir := t.TempDir()
	err := os.WriteFile(filepath.Join(dir, "client.yml"), []byte("nats_url: nats://file:4222\n"), 0644)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("MUC_NATS_URL", "nats://env:4222")
	cfg := loadClientConfigFromPaths([]string{dir})
	if cfg.NATSURL != "nats://env:4222" {
		t.Errorf("NATSURL = %q, want %q", cfg.NATSURL, "nats://env:4222")
	}
}

// TestLoadClientConfig_RemoteUpdatesDefaultOff pins the opt-in: a client with no
// configuration must never accept an update command, whatever the server offers.
func TestLoadClientConfig_RemoteUpdatesDefaultOff(t *testing.T) {
	cfg := loadClientConfigFromPaths([]string{t.TempDir()})

	if cfg.AllowRemoteUpdates {
		t.Error("AllowRemoteUpdates = true by default; remote updates must be opt-in")
	}
	if cfg.UpdateCommand != "" {
		t.Errorf("UpdateCommand = %q, want empty (auto-detect)", cfg.UpdateCommand)
	}
}

func TestLoadClientConfig_RemoteUpdatesFromFile(t *testing.T) {
	dir := t.TempDir()
	body := "allow_remote_updates: true\nupdate_command: /usr/local/bin/upd\n"
	if err := os.WriteFile(filepath.Join(dir, "client.yml"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	cfg := loadClientConfigFromPaths([]string{dir})

	if !cfg.AllowRemoteUpdates {
		t.Error("AllowRemoteUpdates = false, want true from the config file")
	}
	if cfg.UpdateCommand != "/usr/local/bin/upd" {
		t.Errorf("UpdateCommand = %q, want %q", cfg.UpdateCommand, "/usr/local/bin/upd")
	}
}

func TestLoadClientConfig_RemoteUpdatesFromEnv(t *testing.T) {
	t.Setenv("MUC_ALLOW_REMOTE_UPDATES", "true")

	cfg := loadClientConfigFromPaths([]string{t.TempDir()})

	if !cfg.AllowRemoteUpdates {
		t.Error("AllowRemoteUpdates = false, want true from MUC_ALLOW_REMOTE_UPDATES")
	}
}
