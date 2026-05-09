package main

import (
	"log/slog"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	NATSURL    string
	NATSPort   int
	DBPath     string
	HTTPPort   string
	ConsulURL  string
	ConsulTags []string
}

func LoadConfig() Config {
	v := viper.New()

	// Set defaults
	v.SetDefault("nats_url", "embedded")
	v.SetDefault("nats_port", 4222)
	v.SetDefault("db_path", "systems.db")
	v.SetDefault("http_port", "8080")
	v.SetDefault("consul_url", "http://localhost:8500")
	v.SetDefault("consul_tags", []string{})

	// Read from config.yml if present
	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath(".")
	v.AddConfigPath("/etc/muc")
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			slog.Warn("Error reading config file", "error", err)
		}
	} else {
		slog.Info("Loaded config file", "file", v.ConfigFileUsed())
	}

	// Read from .env file if present (overrides config.yml)
	v.SetConfigName(".env")
	v.SetConfigType("env")
	v.AddConfigPath(".")
	if err := v.MergeInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			slog.Warn("Error reading .env file", "error", err)
		}
	}

	// Environment variables override everything
	v.SetEnvPrefix("MUC")
	v.AutomaticEnv()

	// Parse consul_tags from comma-separated string if provided as env var
	var consulTags []string
	tagsVal := v.GetString("consul_tags")
	if tagsVal != "" {
		for _, tag := range strings.Split(tagsVal, ",") {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				consulTags = append(consulTags, tag)
			}
		}
	} else {
		consulTags = v.GetStringSlice("consul_tags")
	}

	config := Config{
		NATSURL:    v.GetString("nats_url"),
		NATSPort:   v.GetInt("nats_port"),
		DBPath:     v.GetString("db_path"),
		HTTPPort:   v.GetString("http_port"),
		ConsulURL:  v.GetString("consul_url"),
		ConsulTags: consulTags,
	}

	slog.Info("Loaded configuration",
		"nats_url", config.NATSURL,
		"nats_port", config.NATSPort,
		"db_path", config.DBPath,
		"http_port", config.HTTPPort,
		"consul_url", config.ConsulURL,
		"consul_tags", config.ConsulTags,
	)
	return config
}
