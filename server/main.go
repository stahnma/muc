package main

import (
	"log/slog"
	"os"
	"os/signal"
	"server/consul"
	"server/metrics"
	"server/nats"
	"server/storage"
	"server/web"
	"syscall"
	"time"

	natsServer "github.com/nats-io/nats-server/v2/server"
)

func main() {
	// Setup logger with CLI flags
	_ = setupLogger()

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

	// Start NATS subscriber in a separate goroutine
	go func() {
		slog.Info("Starting NATS subscriber...")
		nats.StartSubscriber(store, natsURL)
	}()

	// Start the web server
	slog.Info("Starting web server...")
	go web.StartWebServer(store, config.HTTPPort)

	// Register with Consul
	deregisterConsul, err := consul.Register(config.ConsulURL, config.HTTPPort, config.NATSPort, config.ConsulTags)
	if err != nil {
		slog.Warn("Failed to register with Consul, service will run without service discovery", "error", err)
	} else {
		defer deregisterConsul()
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
