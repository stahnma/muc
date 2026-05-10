package main

import (
	"log/slog"

	"github.com/spf13/viper"
)

type ClientConfig struct {
	NATSURL  string
	NATSPort string
}

func LoadClientConfig() ClientConfig {
	return loadClientConfigFromPaths([]string{".", "/etc/muc"})
}

func loadClientConfigFromPaths(paths []string) ClientConfig {
	v := viper.New()

	v.SetDefault("nats_url", "")
	v.SetDefault("nats_port", "4222")

	v.SetConfigName("client")
	v.SetConfigType("yaml")
	for _, p := range paths {
		v.AddConfigPath(p)
	}

	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			slog.Warn("Error reading client config file", "error", err)
		}
	} else {
		slog.Info("Loaded client config file", "file", v.ConfigFileUsed())
	}

	v.SetEnvPrefix("MUC")
	v.AutomaticEnv()

	return ClientConfig{
		NATSURL:  v.GetString("nats_url"),
		NATSPort: v.GetString("nats_port"),
	}
}
