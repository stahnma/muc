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
