package discovery

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
)

const defaultNATSPort = "4222"

// DiscoverNATSServer attempts to discover the NATS server using:
// 1. Environment variable MUC_NATS_URL (explicit override)
// 2. DNS lookup (A record for muc-nats.service.consul, etc.)
// 3. DNS SRV record lookup (for port discovery)
// If none work, it returns an error.
func DiscoverNATSServer() (string, error) {
	if natsURL := os.Getenv("MUC_NATS_URL"); natsURL != "" {
		slog.Info("Using MUC_NATS_URL from environment", "url", natsURL)
		return natsURL, nil
	}

	port := os.Getenv("MUC_NATS_PORT")
	if port == "" {
		port = defaultNATSPort
	}

	serviceNames, domains := buildLookupLists()

	// Try DNS A record lookup first
	if url := discoverViaARecord(serviceNames, domains, port); url != "" {
		slog.Info("Discovered NATS server via DNS A record", "url", url)
		return url, nil
	}

	// Fall back to SRV records (which also provide port)
	if url := discoverViaSRV(serviceNames, domains); url != "" {
		slog.Info("Discovered NATS server via DNS SRV record", "url", url)
		return url, nil
	}

	return "", fmt.Errorf("could not discover NATS server: set MUC_NATS_URL or configure DNS for muc-nats.service.consul")
}

func buildLookupLists() ([]string, []string) {
	serviceNames := []string{"muc-nats", "nats"}
	if envService := os.Getenv("MUC_NATS_DISCOVERY_SERVICE"); envService != "" {
		serviceNames = []string{envService}
	}

	var domains []string
	if domain := os.Getenv("MUC_NATS_DISCOVERY_DOMAIN"); domain != "" {
		domains = []string{domain}
	} else {
		domains = append(domains, "service.consul", "service.dc1.consul")

		hostname, _ := os.Hostname()
		if hostname != "" {
			parts := strings.Split(hostname, ".")
			if len(parts) > 1 {
				domain := strings.Join(parts[1:], ".")
				domains = append(domains, domain)
			}
		}
		domains = append(domains, "local", "lan", "home.arpa")
	}

	return serviceNames, domains
}

// discoverViaARecord tries to resolve service names as plain A records
// (e.g., muc-nats.service.consul) and returns a NATS URL using the given port.
func discoverViaARecord(serviceNames, domains []string, port string) string {
	for _, serviceName := range serviceNames {
		for _, domain := range domains {
			fqdn := fmt.Sprintf("%s.%s", serviceName, domain)
			slog.Debug("Attempting DNS A record lookup", "host", fqdn)

			ips, err := net.LookupHost(fqdn)
			if err != nil {
				slog.Debug("DNS A record lookup failed", "host", fqdn, "error", err)
				continue
			}

			if len(ips) == 0 {
				continue
			}

			url := fmt.Sprintf("nats://%s:%s", ips[0], port)
			slog.Debug("Resolved NATS server from DNS A record", "url", url, "host", fqdn)
			return url
		}
	}
	return ""
}

// discoverViaSRV tries DNS SRV record lookups which provide both host and port.
func discoverViaSRV(serviceNames, domains []string) string {
	for _, serviceName := range serviceNames {
		for _, domain := range domains {
			service := fmt.Sprintf("_%s._tcp.%s", serviceName, domain)
			slog.Debug("Attempting DNS SRV lookup", "service", service)

			_, addrs, err := net.LookupSRV(serviceName, "tcp", domain)
			if err != nil {
				slog.Debug("DNS SRV lookup failed", "service", service, "error", err)
				continue
			}

			if len(addrs) == 0 {
				continue
			}

			addr := addrs[0]
			if len(addrs) > 1 {
				for i := 1; i < len(addrs); i++ {
					if addrs[i].Priority < addr.Priority {
						addr = addrs[i]
					} else if addrs[i].Priority == addr.Priority && addrs[i].Weight > addr.Weight {
						addr = addrs[i]
					}
				}
			}

			target := strings.TrimSuffix(addr.Target, ".")
			ips, err := net.LookupIP(target)
			if err != nil {
				slog.Debug("Failed to resolve SRV target to IP", "target", target, "error", err)
				continue
			}

			if len(ips) == 0 {
				continue
			}

			var ip string
			for _, candidateIP := range ips {
				if candidateIP.To4() != nil {
					ip = candidateIP.String()
					break
				}
			}
			if ip == "" {
				ip = ips[0].String()
			}

			url := fmt.Sprintf("nats://%s:%d", ip, addr.Port)
			slog.Debug("Resolved NATS server from DNS SRV", "url", url, "target", target, "port", addr.Port)
			return url
		}
	}
	return ""
}
