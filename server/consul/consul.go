package consul

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"

	consulapi "github.com/hashicorp/consul/api"
)

const (
	ServiceName     = "muc"
	NATSServiceName = "muc-nats"
)

// Register registers this service with Consul and returns a deregistration function.
// It registers both the HTTP service (as "muc") and the NATS service (as "muc-nats")
// so clients can discover either endpoint.
func Register(consulURL, httpPort string, natsPort int) (deregister func(), err error) {
	config := consulapi.DefaultConfig()
	config.Address = consulURL

	client, err := consulapi.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("creating consul client: %w", err)
	}

	port, err := strconv.Atoi(httpPort)
	if err != nil {
		return nil, fmt.Errorf("parsing HTTP port: %w", err)
	}

	hostname, err := os.Hostname()
	if err != nil {
		hostname = "unknown"
	}

	httpServiceID := fmt.Sprintf("%s-%s", ServiceName, hostname)
	natsServiceID := fmt.Sprintf("%s-%s", NATSServiceName, hostname)

	// Register the HTTP service
	httpRegistration := &consulapi.AgentServiceRegistration{
		ID:   httpServiceID,
		Name: ServiceName,
		Port: port,
		Check: &consulapi.AgentServiceCheck{
			HTTP:                           fmt.Sprintf("http://localhost:%s/api/systems", httpPort),
			Interval:                       "10s",
			Timeout:                        "5s",
			DeregisterCriticalServiceAfter: "90s",
		},
	}

	if err := client.Agent().ServiceRegister(httpRegistration); err != nil {
		return nil, fmt.Errorf("registering HTTP service with consul: %w", err)
	}
	slog.Info("Registered HTTP service with Consul", "service_id", httpServiceID, "consul_url", consulURL, "port", port)

	// Register the NATS service
	natsRegistration := &consulapi.AgentServiceRegistration{
		ID:   natsServiceID,
		Name: NATSServiceName,
		Port: natsPort,
		Check: &consulapi.AgentServiceCheck{
			TCP:                            fmt.Sprintf("localhost:%d", natsPort),
			Interval:                       "10s",
			Timeout:                        "5s",
			DeregisterCriticalServiceAfter: "90s",
		},
	}

	if err := client.Agent().ServiceRegister(natsRegistration); err != nil {
		// Deregister HTTP service if NATS registration fails
		client.Agent().ServiceDeregister(httpServiceID)
		return nil, fmt.Errorf("registering NATS service with consul: %w", err)
	}
	slog.Info("Registered NATS service with Consul", "service_id", natsServiceID, "consul_url", consulURL, "port", natsPort)

	return func() {
		for _, id := range []string{httpServiceID, natsServiceID} {
			if err := client.Agent().ServiceDeregister(id); err != nil {
				slog.Error("Failed to deregister from Consul", "service_id", id, "error", err)
			} else {
				slog.Info("Deregistered service from Consul", "service_id", id)
			}
		}
	}, nil
}
