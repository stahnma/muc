package main

import (
	"log/slog"

	"github.com/spf13/viper"
)

type ClientConfig struct {
	NATSURL  string
	NATSPort string
	// AllowRemoteUpdates is the opt-in for "run updates from the dashboard".
	// It lives here, on the host that would be patched, rather than only on the
	// server: a client that has not opted in never subscribes to the command
	// subject, so no server configuration can make it install anything.
	AllowRemoteUpdates bool
	// UpdateCommand overrides the update script to run. Empty means "find the
	// packaged upd script"; see resolveUpdateCommand.
	UpdateCommand string
	// AllowRemoteReboot is the opt-in for "reboot from the dashboard". It is
	// separate from AllowRemoteUpdates on purpose: patching a host and bouncing
	// it are different decisions, and a host running something that must not
	// be interrupted can allow the first without the second.
	AllowRemoteReboot bool
}

func LoadClientConfig() ClientConfig {
	return loadClientConfigFromPaths([]string{".", "/etc/muc"})
}

func loadClientConfigFromPaths(paths []string) ClientConfig {
	v := viper.New()

	v.SetDefault("nats_url", "")
	v.SetDefault("nats_port", "4222")
	v.SetDefault("allow_remote_updates", false)
	v.SetDefault("update_command", "")
	v.SetDefault("allow_remote_reboot", false)

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
		NATSURL:            v.GetString("nats_url"),
		NATSPort:           v.GetString("nats_port"),
		AllowRemoteUpdates: v.GetBool("allow_remote_updates"),
		UpdateCommand:      v.GetString("update_command"),
		AllowRemoteReboot:  v.GetBool("allow_remote_reboot"),
	}
}
