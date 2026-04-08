package discovery

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// DiscoverNATSServer attempts to discover the NATS server using multiple methods:
// 1. Environment variable MUC_NATS_URL (explicit override)
// 2. DNS SRV record lookup (tries _muc-server._tcp, _muc-nats._tcp, _nats._tcp in order)
// 3. Consul service discovery (if available)
// 4. Environment variable MUC_NATS_SERVER_IP (fallback)
// 5. Default hardcoded IP (last resort)
func DiscoverNATSServer() string {
	// Priority 1: Explicit MUC_NATS_URL override
	if natsURL := os.Getenv("MUC_NATS_URL"); natsURL != "" {
		slog.Info("Using MUC_NATS_URL from environment", "url", natsURL)
		return natsURL
	}

	// Priority 2: Try DNS SRV record lookup
	if url := discoverViaDNS(); url != "" {
		slog.Info("Discovered NATS server via DNS SRV record", "url", url)
		return url
	}

	// Priority 3: Try Consul service discovery
	if url := discoverViaConsul(); url != "" {
		slog.Info("Discovered NATS server via Consul", "url", url)
		return url
	}

	// Priority 4: Environment variable MUC_NATS_SERVER_IP
	serverIP := os.Getenv("MUC_NATS_SERVER_IP")
	if serverIP == "" {
		serverIP = "192.168.1.70" // Default fallback
	}

	// Default port
	port := os.Getenv("MUC_NATS_PORT")
	if port == "" {
		port = "4222"
	}

	url := fmt.Sprintf("nats://%s:%s", serverIP, port)
	slog.Info("Using NATS server from IP configuration", "url", url, "source", "environment_or_default")
	return url
}

// discoverViaDNS attempts to discover the NATS server using DNS SRV records.
// It tries service names in order of specificity (most specific first):
// 1. _muc-server._tcp (most specific to this application)
// 2. _muc-nats._tcp
// 3. _nats._tcp (generic NATS service)
// The domain can be specified via MUC_NATS_DISCOVERY_DOMAIN environment variable,
// or it will try common defaults.
func discoverViaDNS() string {
	// Service names to try, in order of specificity (most specific first)
	serviceNames := []string{"muc-server", "muc-nats", "nats"}
	if envService := os.Getenv("MUC_NATS_DISCOVERY_SERVICE"); envService != "" {
		// If explicitly set, use only that service name
		serviceNames = []string{envService}
	}

	// Get domain from environment or use defaults
	domains := []string{}
	if domain := os.Getenv("MUC_NATS_DISCOVERY_DOMAIN"); domain != "" {
		domains = []string{domain}
	} else {
		// Try common domain patterns
		hostname, _ := os.Hostname()
		if hostname != "" {
			// Try to extract domain from hostname
			parts := strings.Split(hostname, ".")
			if len(parts) > 1 {
				// Use domain from hostname (e.g., hostname.example.com -> example.com)
				domain := strings.Join(parts[1:], ".")
				domains = append(domains, domain)
			}
		}
		// Also try common local domain patterns
		domains = append(domains, "local", "lan", "home.arpa")
	}

	// Try each service name (most specific first)
	for _, serviceName := range serviceNames {
		// Try each domain for this service name
		for _, domain := range domains {
			// Look for SRV record
			service := fmt.Sprintf("_%s._tcp.%s", serviceName, domain)
			slog.Debug("Attempting DNS SRV lookup", "service", service)

			_, addrs, err := net.LookupSRV(serviceName, "tcp", domain)
			if err != nil {
				slog.Debug("DNS SRV lookup failed", "service", service, "error", err)
				continue
			}

			if len(addrs) == 0 {
				slog.Debug("No SRV records found", "service", service)
				continue
			}

			// SRV records have priority and weight fields that can be used for ordering
			// For now, we'll use the first record, but we could sort by priority/weight
			// Priority: lower is better, Weight: higher is better (within same priority)
			addr := addrs[0]

			// If multiple records, prefer lower priority, then higher weight
			if len(addrs) > 1 {
				// Sort by priority (ascending), then by weight (descending)
				for i := 1; i < len(addrs); i++ {
					if addrs[i].Priority < addr.Priority {
						addr = addrs[i]
					} else if addrs[i].Priority == addr.Priority && addrs[i].Weight > addr.Weight {
						addr = addrs[i]
					}
				}
			}

			// SRV records return target and port
			// We need to resolve the target to an IP address
			target := strings.TrimSuffix(addr.Target, ".")
			ips, err := net.LookupIP(target)
			if err != nil {
				slog.Debug("Failed to resolve SRV target to IP", "target", target, "error", err)
				continue
			}

			if len(ips) == 0 {
				slog.Debug("No IP addresses found for SRV target", "target", target)
				continue
			}

			// Prefer IPv4
			var ip string
			for _, candidateIP := range ips {
				if candidateIP.To4() != nil {
					ip = candidateIP.String()
					break
				}
			}
			if ip == "" && len(ips) > 0 {
				ip = ips[0].String()
			}

			url := fmt.Sprintf("nats://%s:%d", ip, addr.Port)
			slog.Debug("Resolved NATS server from DNS SRV", "url", url, "target", target, "port", addr.Port, "service", serviceName)
			return url
		}
	}

	return ""
}

// discoverViaConsul attempts to discover the NATS server using Consul service discovery.
// It looks for a service named "nats" or "muc-nats" in Consul.
// Consul address can be specified via MUC_CONSUL_HTTP_ADDR or defaults to localhost:8500.
func discoverViaConsul() string {
	// Check if Consul client is available (we'll use HTTP API, no need for full client library)
	consulAddr := os.Getenv("MUC_CONSUL_HTTP_ADDR")
	if consulAddr == "" {
		consulAddr = "localhost:8500"
	}

	// Try to connect to Consul with a short timeout
	conn, err := net.DialTimeout("tcp", consulAddr, 2*time.Second)
	if err != nil {
		slog.Debug("Consul not available", "addr", consulAddr, "error", err)
		return ""
	}
	conn.Close()

	// Consul is available, query it via HTTP API
	// Service names to try
	serviceNames := []string{"nats", "muc-nats", "muc-server"}
	if envService := os.Getenv("MUC_NATS_CONSUL_SERVICE"); envService != "" {
		serviceNames = []string{envService}
	}

	for _, serviceName := range serviceNames {
		url := queryConsulService(consulAddr, serviceName)
		if url != "" {
			return url
		}
	}

	return ""
}

// ConsulServiceEntry represents a service entry from Consul API
type ConsulServiceEntry struct {
	Service struct {
		Address string `json:"Address"`
		Port    int    `json:"Port"`
	} `json:"Service"`
}

// queryConsulService queries Consul for a service and returns the NATS URL.
// This uses Consul's HTTP API directly to avoid adding a dependency.
func queryConsulService(consulAddr, serviceName string) string {
	// Ensure consulAddr has http:// prefix if not present
	baseURL := consulAddr
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}

	// Query Consul health API for passing services
	url := fmt.Sprintf("%s/v1/health/service/%s?passing", baseURL, serviceName)

	client := &http.Client{
		Timeout: 2 * time.Second,
	}

	slog.Debug("Querying Consul for service", "url", url, "service", serviceName)

	resp, err := client.Get(url)
	if err != nil {
		slog.Debug("Failed to query Consul API", "error", err)
		return ""
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Debug("Consul API returned non-200 status", "status", resp.StatusCode)
		return ""
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		slog.Debug("Failed to read Consul response", "error", err)
		return ""
	}

	var entries []ConsulServiceEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		slog.Debug("Failed to parse Consul response", "error", err)
		return ""
	}

	if len(entries) == 0 {
		slog.Debug("No passing services found in Consul", "service", serviceName)
		return ""
	}

	// Use the first passing service
	entry := entries[0]
	address := entry.Service.Address
	if address == "" {
		// If Address is empty, Consul returns the node address, but we'd need
		// to query the node endpoint. For now, try to use localhost or the consul address
		// In practice, services usually have an Address set
		slog.Debug("Service entry has no address, skipping", "service", serviceName)
		return ""
	}

	port := entry.Service.Port
	if port == 0 {
		port = 4222 // Default NATS port
	}

	url = fmt.Sprintf("nats://%s:%d", address, port)
	slog.Debug("Resolved NATS server from Consul", "url", url, "service", serviceName)
	return url
}
