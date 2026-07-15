package main

import (
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/viper"
	"github.com/subosito/gotenv"
)

type Config struct {
	NATSURL        string
	NATSPort       int
	DBPath         string
	HTTPPort       string
	ConsulURL      string
	ConsulTags     []string
	ConsulNATSTags []string
}

func LoadConfig() Config {
	return LoadConfigFromPaths([]string{".", "/etc/muc"})
}

func LoadConfigFromPaths(configPaths []string) Config {
	v := viper.New()

	// Set defaults
	v.SetDefault("nats_url", "embedded")
	v.SetDefault("nats_port", 4222)
	v.SetDefault("db_path", "systems.db")
	v.SetDefault("http_port", "8080")
	v.SetDefault("consul_url", "http://localhost:8500")
	v.SetDefault("consul_tags", "")
	v.SetDefault("consul_nats_tags", "")

	// Read from config.yml if present
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	for _, p := range configPaths {
		v.AddConfigPath(p)
	}
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			slog.Warn("Error reading config file", "error", err)
		}
	} else {
		slog.Info("Loaded config file", "file", v.ConfigFileUsed())
	}

	// Load .env file into environment (supports "export KEY=VALUE" syntax)
	for _, envPath := range []string{"../.env", ".env"} {
		if f, err := os.Open(envPath); err == nil {
			if err := gotenv.Apply(f); err != nil {
				slog.Warn("Error parsing .env file", "path", envPath, "error", err)
			} else {
				slog.Info("Loaded .env file", "path", envPath)
			}
			f.Close()
		}
	}

	// Environment variables override everything
	v.SetEnvPrefix("MUC")
	v.AutomaticEnv()

	// Parse tags: comma-separated string from env/dotenv, or list from YAML.
	// consul_tags applies to the HTTP ("muc") service; consul_nats_tags applies
	// to the NATS ("muc-nats") service and falls back to consul_tags when unset.
	consulTags := parseConsulTags(v.GetString("consul_tags"))
	consulNATSTags := parseConsulTags(v.GetString("consul_nats_tags"))
	if consulNATSTags == nil {
		consulNATSTags = consulTags
	}

	config := Config{
		NATSURL:        v.GetString("nats_url"),
		NATSPort:       v.GetInt("nats_port"),
		DBPath:         v.GetString("db_path"),
		HTTPPort:       v.GetString("http_port"),
		ConsulURL:      v.GetString("consul_url"),
		ConsulTags:     consulTags,
		ConsulNATSTags: consulNATSTags,
	}

	slog.Info("Loaded configuration",
		"nats_url", config.NATSURL,
		"nats_port", config.NATSPort,
		"db_path", config.DBPath,
		"http_port", config.HTTPPort,
		"consul_url", config.ConsulURL,
		"consul_tags", config.ConsulTags,
		"consul_nats_tags", config.ConsulNATSTags,
	)
	return config
}

// parseConsulTags splits a comma-separated tag string into a trimmed slice,
// dropping empty entries. Returns nil when no tags are present.
func parseConsulTags(tagsVal string) []string {
	var tags []string
	for _, tag := range strings.Split(tagsVal, ",") {
		tag = strings.TrimSpace(tag)
		if tag != "" {
			tags = append(tags, tag)
		}
	}
	return tags
}
