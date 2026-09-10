package main

import (
	"log/slog"
	"os"
	"os/signal"
	"server/api"
	"server/consul"
	"server/metrics"
	"server/nats"
	"server/runlog"
	"server/storage"
	"server/web"
	"syscall"
	"time"

	natsServer "github.com/nats-io/nats-server/v2/server"
)

var Version = "dev"

func main() {
	// Setup logger with CLI flags
	_ = setupLogger()

	slog.Info("Starting MUC server", "version", Version)

	// Load configuration
	config := LoadConfig()

	// Initialize storage
	store, err := storage.NewBboltStorage(config.DBPath)
	if err != nil {
		slog.Error("Failed to initialize storage", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	// Determine NATS URL - use embedded server if no external URL is provided
	var natsURL string
	var embeddedServer *natsServer.Server

	if config.NATSURL == "" || config.NATSURL == "embedded" {
		// Start embedded NATS server
		slog.Info("Starting embedded NATS server...")
		embeddedServer, natsURL, err = nats.StartEmbeddedServer(config.NATSPort)
		if err != nil {
			slog.Error("Failed to start embedded NATS server", "error", err)
			os.Exit(1)
		}
		defer func() {
			slog.Info("Shutting down embedded NATS server...")
			embeddedServer.Shutdown()
		}()
	} else {
		// Use external NATS server
		natsURL = config.NATSURL
		slog.Info("Using external NATS server", "url", natsURL)
	}

	// Connect to NATS and start the subscriber
	conn, err := nats.Connect(natsURL)
	if err != nil {
		slog.Error("Failed to connect to NATS", "error", err)
		os.Exit(1)
	}
	defer conn.Close()

	// Live output of update runs in flight. In memory by design: it is
	// commentary on a run, and the tail worth keeping is saved with the system
	// when the run ends.
	runs := runlog.New()

	slog.Info("Starting NATS subscriber...")
	if err := conn.StartSubscriber(store, runs); err != nil {
		slog.Error("Failed to subscribe to NATS subjects", "error", err)
		os.Exit(1)
	}

	// Remote updates are off unless this server is configured to offer them.
	// The hosts themselves opt in separately, and theirs is the opt-in that
	// actually gates anything — this one decides whether the dashboard exposes
	// the button and the route at all.
	var updater api.UpdateRequester
	if config.RemoteUpdates {
		updater = conn
		slog.Warn("Remote updates are enabled: hosts that have opted in can be patched from the dashboard")
	} else {
		slog.Info("Remote updates are disabled (set remote_updates: true to enable them)")
	}

	// Start the web server
	slog.Info("Starting web server...")
	go web.StartWebServer(store, config.HTTPPort, Version, updater, conn, runs)

	// Register with Consul in the background. Registration retries until it
	// succeeds and is re-asserted afterward, so a Consul agent that is down at
	// startup (or restarted with a cleared data dir) delays discovery instead
	// of leaving the server unroutable for the life of the process.
	registrar, err := consul.New(config.ConsulURL, config.HTTPPort, config.NATSPort, config.ConsulTags, config.ConsulNATSTags)
	switch {
	case err != nil:
		slog.Error("Consul registration disabled by a configuration error, service will not be discoverable", "error", err)
	case registrar == nil:
		slog.Info("No consul_url configured, running without service discovery")
	default:
		registrar.Start()
		defer registrar.Stop()
	}

	// Start business metrics updater
	go func() {
		ticker := time.NewTicker(30 * time.Second) // Update every 30 seconds
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				systems, err := store.GetAllSystems()
				if err != nil {
					slog.Error("Failed to update business metrics", "error", err)
					continue
				}

				metrics.SystemsMonitored.Set(float64(len(systems)))

				systemsWithUpdates := 0
				totalUpdates := 0
				for _, system := range systems {
					if system.UpdatesAvailable {
						systemsWithUpdates++
						totalUpdates += len(system.PendingUpdates)
					}
				}

				metrics.SystemsWithUpdates.Set(float64(systemsWithUpdates))
				metrics.TotalPendingUpdates.Set(float64(totalUpdates))
			}
		}
	}()

	// Wait for interrupt signal to gracefully shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
	<-sigChan
	slog.Info("Shutting down...")
}
