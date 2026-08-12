package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/nats-io/nats.go"
	"github.com/stahnma/muc/client/discovery"
	"github.com/stahnma/muc/client/hostinfo"
	"github.com/stahnma/muc/client/metrics"
	"github.com/stahnma/muc/client/updates"
)

var Version = "dev"

// recheckSettleDelay is how long to wait after the last SIGUSR1 before
// re-checking. A single `dnf upgrade` writes the rpm database many times, and
// the last write lands before rpm has finished running scriptlets, so a short
// wait both coalesces the burst into one check and lets the transaction finish.
const recheckSettleDelay = 30 * time.Second

// getEnv gets an environment variable or returns a default value
func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

type System struct {
	Hostname            string           `json:"hostname"`
	Architecture        string           `json:"architecture"`
	Ip                  string           `json:"ip"`
	OS                  string           `json:"os"`
	OSVersion           string           `json:"os_version"`
	UpdatesAvailable    bool             `json:"updates_available"`
	UpdateStatusUnknown bool             `json:"update_status_unknown"`
	PendingUpdates      []updates.Update `json:"pending_updates"`
	// UpdatesCheckedAt is when the package-manager query actually ran, in UTC.
	// The server stamps its own LastSeen on receipt, which only says the host is
	// reachable — this says how old the update data itself is, so a host running
	// on hours-old repo metadata is distinguishable from a freshly checked one.
	UpdatesCheckedAt string `json:"updates_checked_at"`
	// UpdateCheckWarnings describes ways the check was incomplete but did not
	// outright fail — repositories the package manager silently skipped, for
	// instance. The count is then a lower bound rather than an answer.
	UpdateCheckWarnings []string `json:"update_check_warnings,omitempty"`
	ClientVersion       string   `json:"client_version"`
	CPUModel            string   `json:"cpu_model"`
	CPUCores            int      `json:"cpu_cores"`
	MemoryTotalBytes    uint64   `json:"memory_total_bytes"`
	UptimeSeconds       uint64   `json:"uptime_seconds"`
	RebootRequired      bool     `json:"reboot_required"`
	// Tailscale is omitted entirely for hosts with no tailnet membership to
	// report, which is what keeps the dashboard silent about tailnets for
	// fleets that do not use one.
	Tailscale *hostinfo.TailscaleInfo `json:"tailscale,omitempty"`
}

// Collects all system data to prepare for publishing
func collectSystemData() (System, error) {
	var system System

	// Hostname
	hostname, err := os.Hostname()
	if err != nil {
		return system, err
	}
	system.Hostname = hostname
	system.ClientVersion = Version

	// Architecture
	arch, err := exec.Command("uname", "-m").Output()
	if err != nil {
		return system, err
	}
	system.Architecture = strings.TrimSpace(string(arch))

	// OS and Version
	system.OS = runtime.GOOS
	if runtime.GOOS == "darwin" {
		// macOS version
		out, err := exec.Command("sw_vers", "-productVersion").Output()
		if err != nil {
			slog.Error("Failed to get macOS version", "error", err)
		} else {
			system.OSVersion = strings.TrimSpace(string(out))
		}
	} else if runtime.GOOS == "linux" {
		// Linux OS version
		if _, err := os.Stat("/etc/os-release"); err == nil {
			content, _ := os.ReadFile("/etc/os-release")
			lines := strings.Split(string(content), "\n")
			for _, line := range lines {
				if strings.HasPrefix(line, "PRETTY_NAME=") {
					system.OS = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), `"`)
				}
			}
		}
	}

	// IP Address
	ip, err := getIPAddress()
	if err != nil {
		slog.Error("Failed to get IP address", "error", err)
	} else {
		system.Ip = ip
	}

	// Pending Updates
	updateStart := time.Now()
	updateResult := updates.GetPendingUpdates()
	updateDuration := time.Since(updateStart).Seconds()

	// Record update check duration (we'll use "unknown" if no manager detected)
	packageManager := "unknown"
	if updateResult.ManagerDetected {
		if runtime.GOOS == "darwin" {
			packageManager = "brew"
		} else if runtime.GOOS == "linux" {
			// Try to detect which Linux package manager was used
			// This is approximate - we check which one detected updates
			packageManager = "linux"
		}
	}
	metrics.UpdateCheckDuration.WithLabelValues(packageManager).Observe(updateDuration)

	system.PendingUpdates = updateResult.Updates
	system.UpdatesAvailable, system.UpdateStatusUnknown = updateResult.Status()
	system.UpdatesCheckedAt = time.Now().UTC().Format(time.RFC3339)
	for _, repo := range updateResult.SkippedRepos {
		system.UpdateCheckWarnings = append(system.UpdateCheckWarnings,
			"repository skipped, update list may be incomplete: "+repo)
	}

	// Hardware, uptime, and reboot-required status (best-effort)
	hw := hostinfo.Collect()
	system.CPUModel = hw.CPUModel
	system.CPUCores = hw.CPUCores
	system.MemoryTotalBytes = hw.MemoryTotalBytes
	system.UptimeSeconds = hw.UptimeSeconds
	system.RebootRequired = hw.RebootRequired
	system.Tailscale = hw.Tailscale

	return system, nil
}

// Publishes system data to NATS
func sendSystemUpdate(nc *nats.Conn) {
	start := time.Now()
	defer func() {
		duration := time.Since(start).Seconds()
		metrics.NATSPublishDuration.Observe(duration)
	}()

	// Collect system data
	system, err := collectSystemData()
	if err != nil {
		slog.Error("Failed to collect system data", "error", err)
		return
	}

	// Update pending updates count metric
	metrics.PendingUpdatesCount.Set(float64(len(system.PendingUpdates)))

	// Marshal system data to JSON
	data, err := json.Marshal(system)
	if err != nil {
		slog.Error("Failed to marshal system data", "error", err)
		return
	}

	// Enhanced logging for NATS publishing
	slog.Debug("NATS Publishing Details",
		"nats_url", nc.ConnectedUrl(),
		"client_id", nc.ConnectedClusterName(),
		"reconnects", nc.Stats().Reconnects,
		"messages_in", nc.Stats().InMsgs,
		"messages_out", nc.Stats().OutMsgs,
		"bytes_in", nc.Stats().InBytes,
		"bytes_out", nc.Stats().OutBytes,
		"message_size", len(data),
		"hostname", system.Hostname,
		"ip", system.Ip,
		"updates_available", system.UpdatesAvailable,
		"update_status_unknown", system.UpdateStatusUnknown,
		"update_count", len(system.PendingUpdates))

	// Publish the message
	subject := "systems.updates." + system.Hostname
	slog.Debug("Publishing to subject", "subject", subject, "payload", string(data))

	// Set a context with timeout for the publish operation
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Try to publish with timeout
	// Use buffered channel and ensure goroutine always completes to prevent leaks
	publishChan := make(chan error, 1)
	go func() {
		// Check if context is already cancelled before publishing
		select {
		case <-ctx.Done():
			publishChan <- ctx.Err()
			return
		default:
		}
		publishChan <- nc.Publish(subject, data)
	}()

	select {
	case err := <-publishChan:
		if err != nil {
			if err == ctx.Err() {
				slog.Error("Publish cancelled due to timeout")
			} else {
				slog.Error("Failed to publish message to NATS", "error", err)
			}
			return
		}
		metrics.NATSMessagesPublished.Inc()
		slog.Info("Successfully published to subject", "subject", subject)

		// Try to flush with timeout using context
		flushCtx, flushCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer flushCancel()

		flushChan := make(chan error, 1)
		go func() {
			// Check if context is already cancelled before flushing
			select {
			case <-flushCtx.Done():
				flushChan <- flushCtx.Err()
				return
			default:
			}
			flushChan <- nc.Flush()
		}()

		select {
		case err := <-flushChan:
			if err != nil {
				if err == flushCtx.Err() {
					slog.Error("Flush timeout after 5 seconds")
				} else {
					slog.Error("Failed to flush NATS connection", "error", err)
				}
			} else {
				slog.Debug("Successfully flushed NATS connection")
			}
		case <-flushCtx.Done():
			slog.Error("Flush timeout after 5 seconds")
			// Drain the channel in a non-blocking way to prevent goroutine leak
			select {
			case <-flushChan:
			default:
			}
		}

	case <-ctx.Done():
		slog.Error("Publish timeout after 10 seconds")
		// Drain the channel in a non-blocking way to prevent goroutine leak
		// The goroutine will complete when nc.Publish() returns, but we don't wait
		select {
		case <-publishChan:
		default:
		}
	}
}

// Finds the external IP address of the system
func getIPAddress() (string, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "", err
	}

	for _, addr := range addrs {
		// Check if the address is an IP network
		if ipNet, ok := addr.(*net.IPNet); ok && !ipNet.IP.IsLoopback() {
			ip := ipNet.IP
			// Ensure it's IPv4 and not a link-local address
			if ip.To4() != nil && !ip.IsLinkLocalUnicast() {
				return ip.String(), nil
			}
		}
	}

	return "", fmt.Errorf("no valid external IP address found")
}

func main() {
	// Setup logger with CLI flags (also handles --version)
	_ = setupLogger()

	// Load optional config file (/etc/muc/client.yml or ./client.yml)
	cfg := LoadClientConfig()

	// Determine NATS URL: config/env takes priority, then discovery
	var natsURL string
	if cfg.NATSURL != "" {
		slog.Info("Using NATS URL from config", "url", cfg.NATSURL)
		natsURL = cfg.NATSURL
	} else {
		if cfg.NATSPort != "" && cfg.NATSPort != "4222" {
			os.Setenv("MUC_NATS_PORT", cfg.NATSPort)
		}
		var err error
		natsURL, err = discovery.DiscoverNATSServer()
		if err != nil {
			slog.Error("Failed to discover NATS server", "error", err)
			os.Exit(1)
		}
	}

	// Configure exponential backoff: start at 5s, max 10 minutes
	// Pattern: 5s, 10s, 20s, 40s, 80s, 160s, 320s, 600s (capped)
	backoffConfig := backoff.NewExponentialBackOff()
	backoffConfig.InitialInterval = 5 * time.Second
	backoffConfig.MaxInterval = 10 * time.Minute
	backoffConfig.Multiplier = 2.0
	backoffConfig.MaxElapsedTime = 10 * time.Minute
	backoffConfig.RandomizationFactor = 0

	var nc *nats.Conn
	connectFunc := func() error {
		slog.Info("Attempting to connect to NATS", "url", natsURL)
		var err error
		nc, err = nats.Connect(natsURL,
			nats.Name("System Updates Publisher"),
			nats.Timeout(30*time.Second),      // Increased timeout
			nats.PingInterval(20*time.Second), // Add periodic ping
			nats.MaxPingsOutstanding(5),       // Allow 5 outstanding pings
			nats.MaxReconnects(-1),            // Unlimited reconnections
			nats.ReconnectWait(5*time.Second), // Wait 5 seconds between reconnection attempts
			nats.ReconnectHandler(func(nc *nats.Conn) {
				metrics.NATSReconnects.Inc()
				metrics.NATSConnectionStatus.Set(1)
				slog.Info("Reconnected to NATS", "url", nc.ConnectedUrl())
				slog.Debug("Connection statistics",
					"reconnects", nc.Stats().Reconnects,
					"messages_in", nc.Stats().InMsgs,
					"messages_out", nc.Stats().OutMsgs)
			}),
			nats.DisconnectErrHandler(func(nc *nats.Conn, err error) {
				metrics.NATSConnectionStatus.Set(0)
				if err != nil {
					slog.Error("Disconnected from NATS due to error", "error", err)
				} else {
					slog.Info("Disconnected from NATS")
				}
			}),
			nats.ClosedHandler(func(nc *nats.Conn) {
				metrics.NATSConnectionStatus.Set(0)
				slog.Info("Connection to NATS closed", "reason", nc.LastError())
				slog.Debug("Final connection statistics",
					"reconnects", nc.Stats().Reconnects,
					"messages_in", nc.Stats().InMsgs,
					"messages_out", nc.Stats().OutMsgs)
			}),
		)
		if err != nil {
			return err
		}

		// Verify connection is actually ready by flushing
		// This ensures the connection handshake is complete
		if err := nc.FlushTimeout(5 * time.Second); err != nil {
			nc.Close()
			return fmt.Errorf("connection established but flush failed (connection not ready): %w", err)
		}

		return nil
	}

	// Retry connection with exponential backoff
	notifyFunc := func(err error, duration time.Duration) {
		slog.Warn("Connection attempt failed, retrying", "error", err, "retry_in", duration)
	}

	err := backoff.RetryNotify(connectFunc, backoffConfig, notifyFunc)
	if err != nil {
		slog.Error("Failed to connect to NATS after 10 minutes of retrying", "error", err)
		os.Exit(1)
	}
	defer nc.Close()

	metrics.NATSConnectionStatus.Set(1) // Set initial connection status
	slog.Info("Successfully connected to NATS", "url", nc.ConnectedUrl())
	slog.Debug("Server ID", "id", nc.ConnectedServerId())

	// Function to check NATS connection health
	checkConnection := func() bool {
		if !nc.IsConnected() {
			// Log additional diagnostic info
			slog.Debug("Connection check failed",
				"is_connected", nc.IsConnected(),
				"last_error", nc.LastError(),
				"connected_url", nc.ConnectedUrl())
			slog.Warn("NATS connection is not active")
			return false
		}
		return true
	}

	// Send the first update immediately
	if checkConnection() {
		sendSystemUpdate(nc)
	}

	// Run the client as a long-running daemon
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// Add a separate ticker for connection health checks
	healthTicker := time.NewTicker(30 * time.Second)
	defer healthTicker.Stop()

	// SIGUSR1 asks for an out-of-band re-check. muc-client-recheck.path watches
	// the rpm/dpkg databases and reloads us after a package transaction, so an
	// admin running `dnf upgrade` sees the dashboard catch up in seconds instead
	// of waiting out the poll interval above.
	recheck := make(chan os.Signal, 1)
	signal.Notify(recheck, syscall.SIGUSR1)
	defer signal.Stop(recheck)

	// One transaction writes the package database many times, so coalesce a
	// burst of signals into a single check once things settle.
	settle := time.NewTimer(0)
	if !settle.Stop() {
		<-settle.C
	}
	defer settle.Stop()

	for {
		select {
		case <-ticker.C:
			if checkConnection() {
				sendSystemUpdate(nc)
			}
		case <-healthTicker.C:
			checkConnection()
		case <-recheck:
			slog.Debug("Re-check requested; waiting for package transaction to settle",
				"settle", recheckSettleDelay)
			if !settle.Stop() {
				// Drain only if the timer had already fired and nothing has
				// consumed it yet; a stopped-but-unfired timer has nothing to drain.
				select {
				case <-settle.C:
				default:
				}
			}
			settle.Reset(recheckSettleDelay)
		case <-settle.C:
			slog.Info("Package transaction detected; re-checking for updates")
			if checkConnection() {
				sendSystemUpdate(nc)
			}
		}
	}
}
