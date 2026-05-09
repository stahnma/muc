package main

import (
	"log/slog"
	"os"
	"strconv"
)

type Config struct {
	NATSURL   string
	NATSPort  int
	DBPath    string
	HTTPPort  string
	ConsulURL string
}

func LoadConfig() Config {
	// Default to embedded NATS server if MUC_NATS_URL is not set
	natsURL := getEnv("MUC_NATS_URL", "embedded")

	// Parse NATS port for embedded server (default 4222)
	natsPort := 4222
	if portStr := getEnv("MUC_NATS_PORT", ""); portStr != "" {
		if port, err := strconv.Atoi(portStr); err == nil {
			natsPort = port
		}
	}

	config := Config{
		NATSURL:   natsURL,
		NATSPort:  natsPort,
		DBPath:    getEnv("MUC_DB_PATH", "systems.db"),
		HTTPPort:  getEnv("MUC_HTTP_PORT", "8080"),
		ConsulURL: getEnv("CONSUL_URL", "http://localhost:8500"),
	}
	slog.Info("Loaded configuration", "nats_url", config.NATSURL, "nats_port", config.NATSPort, "db_path", config.DBPath, "http_port", config.HTTPPort, "consul_url", config.ConsulURL)
	return config
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
