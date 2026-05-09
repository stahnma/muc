package main

import (
	"os"
	"path/filepath"
	"testing"
)

// clearMUCEnv unsets all MUC_ env vars to isolate tests.
func clearMUCEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"MUC_NATS_URL", "MUC_NATS_PORT", "MUC_DB_PATH",
		"MUC_HTTP_PORT", "MUC_CONSUL_URL", "MUC_CONSUL_TAGS",
	} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

func TestLoadConfig_Defaults(t *testing.T) {
	clearMUCEnv(t)

	// Run from a temp dir so no .env or config.yml is found
	tmp := t.TempDir()
	orig, _ := os.Getwd()
	os.Chdir(tmp)
	defer os.Chdir(orig)

	cfg := LoadConfig()

	if cfg.NATSURL != "embedded" {
		t.Errorf("NATSURL = %q, want %q", cfg.NATSURL, "embedded")
	}
	if cfg.NATSPort != 4222 {
		t.Errorf("NATSPort = %d, want %d", cfg.NATSPort, 4222)
	}
	if cfg.DBPath != "systems.db" {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, "systems.db")
	}
	if cfg.HTTPPort != "8080" {
		t.Errorf("HTTPPort = %q, want %q", cfg.HTTPPort, "8080")
	}
	if cfg.ConsulURL != "http://localhost:8500" {
		t.Errorf("ConsulURL = %q, want %q", cfg.ConsulURL, "http://localhost:8500")
	}
	if len(cfg.ConsulTags) != 0 {
		t.Errorf("ConsulTags = %v, want empty", cfg.ConsulTags)
	}
}

func TestLoadConfig_EnvVarsOverrideDefaults(t *testing.T) {
	clearMUCEnv(t)

	tmp := t.TempDir()
	orig, _ := os.Getwd()
	os.Chdir(tmp)
	defer os.Chdir(orig)

	t.Setenv("MUC_HTTP_PORT", "9090")
	t.Setenv("MUC_DB_PATH", "/tmp/test.db")
	t.Setenv("MUC_CONSUL_URL", "http://consul:8500")
	t.Setenv("MUC_CONSUL_TAGS", "prod,us-east-1")
	t.Setenv("MUC_NATS_PORT", "5222")

	cfg := LoadConfig()

	if cfg.HTTPPort != "9090" {
		t.Errorf("HTTPPort = %q, want %q", cfg.HTTPPort, "9090")
	}
	if cfg.DBPath != "/tmp/test.db" {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, "/tmp/test.db")
	}
	if cfg.ConsulURL != "http://consul:8500" {
		t.Errorf("ConsulURL = %q, want %q", cfg.ConsulURL, "http://consul:8500")
	}
	if cfg.NATSPort != 5222 {
		t.Errorf("NATSPort = %d, want %d", cfg.NATSPort, 5222)
	}
	if len(cfg.ConsulTags) != 2 || cfg.ConsulTags[0] != "prod" || cfg.ConsulTags[1] != "us-east-1" {
		t.Errorf("ConsulTags = %v, want [prod us-east-1]", cfg.ConsulTags)
	}
}

func TestLoadConfig_ConfigYAML(t *testing.T) {
	clearMUCEnv(t)

	tmp := t.TempDir()
	orig, _ := os.Getwd()
	os.Chdir(tmp)
	defer os.Chdir(orig)

	yamlContent := `
nats_url: nats://remote:4222
nats_port: 5222
db_path: /data/muc.db
http_port: "3000"
consul_url: http://consul.local:8500
consul_tags: staging,eu-west-1
`
	os.WriteFile(filepath.Join(tmp, "config.yml"), []byte(yamlContent), 0644)

	cfg := LoadConfig()

	if cfg.NATSURL != "nats://remote:4222" {
		t.Errorf("NATSURL = %q, want %q", cfg.NATSURL, "nats://remote:4222")
	}
	if cfg.NATSPort != 5222 {
		t.Errorf("NATSPort = %d, want %d", cfg.NATSPort, 5222)
	}
	if cfg.DBPath != "/data/muc.db" {
		t.Errorf("DBPath = %q, want %q", cfg.DBPath, "/data/muc.db")
	}
	if cfg.HTTPPort != "3000" {
		t.Errorf("HTTPPort = %q, want %q", cfg.HTTPPort, "3000")
	}
	if cfg.ConsulURL != "http://consul.local:8500" {
		t.Errorf("ConsulURL = %q, want %q", cfg.ConsulURL, "http://consul.local:8500")
	}
	if len(cfg.ConsulTags) != 2 || cfg.ConsulTags[0] != "staging" || cfg.ConsulTags[1] != "eu-west-1" {
		t.Errorf("ConsulTags = %v, want [staging eu-west-1]", cfg.ConsulTags)
	}
}

func TestLoadConfig_DotEnvWithExportPrefix(t *testing.T) {
	clearMUCEnv(t)

	tmp := t.TempDir()
	orig, _ := os.Getwd()
	os.Chdir(tmp)
	defer os.Chdir(orig)

	envContent := "export MUC_HTTP_PORT=8111\nexport MUC_CONSUL_TAGS=caddy\n"
	os.WriteFile(filepath.Join(tmp, ".env"), []byte(envContent), 0644)

	cfg := LoadConfig()

	if cfg.HTTPPort != "8111" {
		t.Errorf("HTTPPort = %q, want %q", cfg.HTTPPort, "8111")
	}
	if len(cfg.ConsulTags) != 1 || cfg.ConsulTags[0] != "caddy" {
		t.Errorf("ConsulTags = %v, want [caddy]", cfg.ConsulTags)
	}
}

func TestLoadConfig_DotEnvWithoutExportPrefix(t *testing.T) {
	clearMUCEnv(t)

	tmp := t.TempDir()
	orig, _ := os.Getwd()
	os.Chdir(tmp)
	defer os.Chdir(orig)

	envContent := "MUC_HTTP_PORT=7777\nMUC_CONSUL_TAGS=web,api\n"
	os.WriteFile(filepath.Join(tmp, ".env"), []byte(envContent), 0644)

	cfg := LoadConfig()

	if cfg.HTTPPort != "7777" {
		t.Errorf("HTTPPort = %q, want %q", cfg.HTTPPort, "7777")
	}
	if len(cfg.ConsulTags) != 2 || cfg.ConsulTags[0] != "web" || cfg.ConsulTags[1] != "api" {
		t.Errorf("ConsulTags = %v, want [web api]", cfg.ConsulTags)
	}
}

func TestLoadConfig_EnvOverridesDotEnv(t *testing.T) {
	clearMUCEnv(t)

	tmp := t.TempDir()
	orig, _ := os.Getwd()
	os.Chdir(tmp)
	defer os.Chdir(orig)

	envContent := "MUC_HTTP_PORT=7777\n"
	os.WriteFile(filepath.Join(tmp, ".env"), []byte(envContent), 0644)

	t.Setenv("MUC_HTTP_PORT", "9999")

	cfg := LoadConfig()

	if cfg.HTTPPort != "9999" {
		t.Errorf("HTTPPort = %q, want %q (env should override .env)", cfg.HTTPPort, "9999")
	}
}

func TestLoadConfig_ConsulTagsSingleValue(t *testing.T) {
	clearMUCEnv(t)

	tmp := t.TempDir()
	orig, _ := os.Getwd()
	os.Chdir(tmp)
	defer os.Chdir(orig)

	t.Setenv("MUC_CONSUL_TAGS", "caddy")

	cfg := LoadConfig()

	if len(cfg.ConsulTags) != 1 || cfg.ConsulTags[0] != "caddy" {
		t.Errorf("ConsulTags = %v, want [caddy]", cfg.ConsulTags)
	}
}

func TestLoadConfig_ConsulTagsEmpty(t *testing.T) {
	clearMUCEnv(t)

	tmp := t.TempDir()
	orig, _ := os.Getwd()
	os.Chdir(tmp)
	defer os.Chdir(orig)

	cfg := LoadConfig()

	if cfg.ConsulTags != nil {
		t.Errorf("ConsulTags = %v, want nil", cfg.ConsulTags)
	}
}
