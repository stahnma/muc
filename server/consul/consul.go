package consul

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	consulapi "github.com/hashicorp/consul/api"
)

const (
	ServiceName     = "muc"
	NATSServiceName = "muc-nats"

	// Retry pacing for an agent that is down or not up yet. The interval
	// doubles after every failed sweep so a Consul that is genuinely absent
	// costs one attempt per maxRetryInterval instead of spinning.
	defaultInitialRetryInterval = 2 * time.Second
	defaultMaxRetryInterval     = 2 * time.Minute

	// How often to confirm the agent still knows about us. An agent that
	// restarts with a cleared data dir forgets every service registered
	// against it, and nothing else would notice.
	defaultReassertInterval = 30 * time.Second
)

// agent is the slice of consulapi.Agent this package uses, so tests can
// substitute an agent that fails on demand.
type agent interface {
	ServiceRegister(reg *consulapi.AgentServiceRegistration) error
	ServiceDeregister(serviceID string) error
	Services() (map[string]*consulapi.AgentService, error)
}

// Registrar keeps this server's HTTP ("muc") and NATS ("muc-nats") services
// asserted in Consul for as long as it runs. Registration happens in the
// background: Consul being down at startup delays discovery, it does not
// disable it for the life of the process.
type Registrar struct {
	agent     agent
	consulURL string
	services  []*consulapi.AgentServiceRegistration

	initialRetryInterval time.Duration
	maxRetryInterval     time.Duration
	reassertInterval     time.Duration

	mu         sync.Mutex
	registered map[string]bool

	cancel context.CancelFunc
	done   chan struct{}
}

// New builds a Registrar for the HTTP and NATS services. It returns a nil
// Registrar (and nil error) when consulURL is empty, which is how service
// discovery is turned off. Nothing here talks to Consul yet; call Start.
func New(consulURL, httpPort string, natsPort int, httpTags, natsTags []string) (*Registrar, error) {
	if consulURL == "" {
		return nil, nil
	}

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

	httpRegistration := &consulapi.AgentServiceRegistration{
		ID:   fmt.Sprintf("%s-%s", ServiceName, hostname),
		Name: ServiceName,
		Port: port,
		Tags: httpTags,
		Check: &consulapi.AgentServiceCheck{
			HTTP:                           fmt.Sprintf("http://localhost:%s/api/systems", httpPort),
			Interval:                       "10s",
			Timeout:                        "5s",
			DeregisterCriticalServiceAfter: "90s",
		},
	}

	natsRegistration := &consulapi.AgentServiceRegistration{
		ID:   fmt.Sprintf("%s-%s", NATSServiceName, hostname),
		Name: NATSServiceName,
		Port: natsPort,
		Tags: natsTags,
		Check: &consulapi.AgentServiceCheck{
			TCP:                            fmt.Sprintf("localhost:%d", natsPort),
			Interval:                       "10s",
			Timeout:                        "5s",
			DeregisterCriticalServiceAfter: "90s",
		},
	}

	return newRegistrar(client.Agent(), consulURL, httpRegistration, natsRegistration), nil
}

func newRegistrar(a agent, consulURL string, services ...*consulapi.AgentServiceRegistration) *Registrar {
	return &Registrar{
		agent:                a,
		consulURL:            consulURL,
		services:             services,
		initialRetryInterval: defaultInitialRetryInterval,
		maxRetryInterval:     defaultMaxRetryInterval,
		reassertInterval:     defaultReassertInterval,
		registered:           make(map[string]bool),
	}
}

// Start begins registering in the background and returns immediately. Stop
// tears the goroutine down and deregisters.
func (r *Registrar) Start() {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.done = make(chan struct{})

	go func() {
		defer close(r.done)
		r.run(ctx)
	}()
}

// Stop halts the background loop and deregisters both services. Each
// deregistration is attempted even if an earlier one fails.
func (r *Registrar) Stop() {
	if r.cancel != nil {
		r.cancel()
		<-r.done
	}

	for _, svc := range r.services {
		if err := r.agent.ServiceDeregister(svc.ID); err != nil {
			slog.Error("Failed to deregister from Consul", "service_id", svc.ID, "error", err)
			continue
		}
		slog.Info("Deregistered service from Consul", "service_id", svc.ID)
	}
}

func (r *Registrar) run(ctx context.Context) {
	retry := r.initialRetryInterval

	for {
		wait := r.reassertInterval
		if r.ensure() {
			retry = r.initialRetryInterval
		} else {
			wait = retry
			if retry *= 2; retry > r.maxRetryInterval {
				retry = r.maxRetryInterval
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// ensure registers every service the agent does not already know about, and
// reports whether all of them are registered. Services are handled
// independently: one failing never skips the others.
func (r *Registrar) ensure() bool {
	// Registering a service the agent already has resets its health check to
	// critical, which would drop us out of discovery for a beat on every
	// sweep, so only register what is actually missing. When the agent won't
	// say what it has, assume nothing and let the register calls report.
	known, err := r.agent.Services()
	if err != nil {
		known = nil
	}

	allRegistered := true
	for _, svc := range r.services {
		if _, present := known[svc.ID]; present {
			r.record(svc, nil)
			continue
		}
		if err := r.agent.ServiceRegister(svc); err != nil {
			r.record(svc, fmt.Errorf("registering %s service with consul: %w", svc.Name, err))
			allRegistered = false
			continue
		}
		r.record(svc, nil)
	}
	return allRegistered
}

// record tracks per-service registration state and logs only on a change, so
// a steady state stays quiet while a failure is loud: a server Consul cannot
// see is a server Caddy will not route to.
func (r *Registrar) record(svc *consulapi.AgentServiceRegistration, err error) {
	r.mu.Lock()
	was := r.registered[svc.ID]
	r.registered[svc.ID] = err == nil
	r.mu.Unlock()

	switch {
	case err != nil:
		slog.Error("Consul registration failed, service is not discoverable until it succeeds; retrying",
			"service_id", svc.ID, "consul_url", r.consulURL, "error", err)
	case !was:
		slog.Info("Registered service with Consul",
			"service_id", svc.ID, "consul_url", r.consulURL, "port", svc.Port, "tags", svc.Tags)
	}
}

// isRegistered reports whether the service is currently believed registered.
func (r *Registrar) isRegistered(serviceID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.registered[serviceID]
}
