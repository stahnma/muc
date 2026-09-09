package consul

import (
	"errors"
	"sync"
	"testing"
	"time"

	consulapi "github.com/hashicorp/consul/api"
)

// fakeAgent stands in for a Consul agent that can be down, selectively broken,
// or forgetful about services it was told about.
type fakeAgent struct {
	mu              sync.Mutex
	services        map[string]*consulapi.AgentService
	servicesErr     error
	registerErr     map[string]error
	deregisterErr   map[string]error
	registerCalls   map[string]int
	deregisterCalls map[string]int
}

func newFakeAgent() *fakeAgent {
	return &fakeAgent{
		services:        map[string]*consulapi.AgentService{},
		registerErr:     map[string]error{},
		deregisterErr:   map[string]error{},
		registerCalls:   map[string]int{},
		deregisterCalls: map[string]int{},
	}
}

func (f *fakeAgent) ServiceRegister(reg *consulapi.AgentServiceRegistration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.registerCalls[reg.ID]++
	if err := f.registerErr[reg.ID]; err != nil {
		return err
	}
	f.services[reg.ID] = &consulapi.AgentService{ID: reg.ID, Service: reg.Name, Port: reg.Port}
	return nil
}

func (f *fakeAgent) ServiceDeregister(serviceID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.deregisterCalls[serviceID]++
	if err := f.deregisterErr[serviceID]; err != nil {
		return err
	}
	delete(f.services, serviceID)
	return nil
}

func (f *fakeAgent) Services() (map[string]*consulapi.AgentService, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.servicesErr != nil {
		return nil, f.servicesErr
	}
	out := make(map[string]*consulapi.AgentService, len(f.services))
	for id, svc := range f.services {
		out[id] = svc
	}
	return out, nil
}

// down makes every call fail, the way an agent that isn't listening does.
func (f *fakeAgent) down(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.servicesErr = err
	for _, id := range []string{httpID, natsID} {
		f.registerErr[id] = err
	}
}

func (f *fakeAgent) up() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.servicesErr = nil
	f.registerErr = map[string]error{}
}

// forget drops a service without deregistering it, as an agent restarted with
// a cleared data dir would.
func (f *fakeAgent) forget(serviceID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.services, serviceID)
}

func (f *fakeAgent) has(serviceID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.services[serviceID]
	return ok
}

func (f *fakeAgent) registerCount(serviceID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.registerCalls[serviceID]
}

func (f *fakeAgent) deregisterCount(serviceID string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.deregisterCalls[serviceID]
}

const (
	httpID = "muc-testhost"
	natsID = "muc-nats-testhost"
)

// testRegistrar builds a Registrar over a fake agent with intervals short
// enough that a test can watch several sweeps go by.
func testRegistrar(a agent) *Registrar {
	r := newRegistrar(a, "http://localhost:8500",
		&consulapi.AgentServiceRegistration{ID: httpID, Name: ServiceName, Port: 8080},
		&consulapi.AgentServiceRegistration{ID: natsID, Name: NATSServiceName, Port: 4222},
	)
	r.initialRetryInterval = time.Millisecond
	r.maxRetryInterval = 5 * time.Millisecond
	r.reassertInterval = time.Millisecond
	return r
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// A failure registering one service must not skip the other.
func TestEnsureRegistersServicesIndependently(t *testing.T) {
	a := newFakeAgent()
	a.registerErr[httpID] = errors.New("connection refused")
	r := testRegistrar(a)

	if r.ensure() {
		t.Fatal("ensure() = true, want false when a service fails to register")
	}
	if a.has(httpID) {
		t.Error("HTTP service registered despite a failing register call")
	}
	if !a.has(natsID) {
		t.Error("NATS service was not registered after the HTTP service failed")
	}
	if r.isRegistered(httpID) {
		t.Error("HTTP service tracked as registered after failure")
	}
	if !r.isRegistered(natsID) {
		t.Error("NATS service not tracked as registered after success")
	}

	// The failing one recovers on the next sweep without disturbing the other.
	a.up()
	if !r.ensure() {
		t.Fatal("ensure() = false, want true once the agent accepts the registration")
	}
	if !a.has(httpID) || !a.has(natsID) {
		t.Errorf("services after recovery: http=%v nats=%v, want both registered", a.has(httpID), a.has(natsID))
	}
	if got := a.registerCount(natsID); got != 1 {
		t.Errorf("NATS register calls = %d, want 1 (already registered, must not be re-registered)", got)
	}
}

// Consul being down at startup must delay registration, not cancel it.
func TestRunRetriesUntilConsulIsReachable(t *testing.T) {
	a := newFakeAgent()
	a.down(errors.New("connection refused"))
	r := testRegistrar(a)

	r.Start()
	defer r.Stop()

	waitFor(t, "the first failed attempt", func() bool { return a.registerCount(httpID) > 0 })
	if a.has(httpID) || a.has(natsID) {
		t.Fatal("services registered while the agent was down")
	}

	a.up()
	waitFor(t, "registration to succeed once the agent is up", func() bool {
		return a.has(httpID) && a.has(natsID)
	})
	if !r.isRegistered(httpID) || !r.isRegistered(natsID) {
		t.Error("registrar did not track both services as registered")
	}
}

// An agent that forgets us (restarted with a cleared data dir) must be told again.
func TestRunReassertsRegistrationAfterAgentForgets(t *testing.T) {
	a := newFakeAgent()
	r := testRegistrar(a)

	r.Start()
	defer r.Stop()

	waitFor(t, "initial registration", func() bool { return a.has(httpID) && a.has(natsID) })
	before := a.registerCount(httpID)

	a.forget(httpID)
	waitFor(t, "re-registration after the agent forgot the service", func() bool { return a.has(httpID) })

	if got := a.registerCount(httpID); got <= before {
		t.Errorf("HTTP register calls = %d, want more than %d", got, before)
	}
	// Re-registering a service the agent still has resets its health check, so
	// the untouched service must be left alone across those same sweeps.
	if got := a.registerCount(natsID); got != 1 {
		t.Errorf("NATS register calls = %d, want 1 while it stayed registered", got)
	}
}

// Empty consul_url means service discovery is off, with nothing running.
func TestNewWithoutConsulURLIsDisabled(t *testing.T) {
	r, err := New("", "8080", 4222, nil, nil)
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}
	if r != nil {
		t.Fatalf("New() = %v, want nil registrar when consul_url is unset", r)
	}
}

func TestNewRejectsBadHTTPPort(t *testing.T) {
	if _, err := New("http://localhost:8500", "not-a-port", 4222, nil, nil); err == nil {
		t.Fatal("New() error = nil, want an error for an unparsable HTTP port")
	}
}

// Shutdown deregisters both services even when the first call fails.
func TestStopDeregistersEveryServiceDespiteFailure(t *testing.T) {
	a := newFakeAgent()
	a.deregisterErr[httpID] = errors.New("connection refused")
	r := testRegistrar(a)

	r.Start()
	waitFor(t, "initial registration", func() bool { return a.has(natsID) })
	r.Stop()

	if got := a.deregisterCount(httpID); got != 1 {
		t.Errorf("HTTP deregister calls = %d, want 1", got)
	}
	if got := a.deregisterCount(natsID); got != 1 {
		t.Errorf("NATS deregister calls = %d, want 1 after the HTTP deregister failed", got)
	}
	if a.has(natsID) {
		t.Error("NATS service still registered after Stop")
	}
}

// Backoff grows to the cap so an absent Consul costs one attempt per interval
// rather than a hot loop.
func TestRunBacksOffWhileConsulIsAbsent(t *testing.T) {
	a := newFakeAgent()
	a.down(errors.New("connection refused"))
	r := testRegistrar(a)
	r.initialRetryInterval = 10 * time.Millisecond
	r.maxRetryInterval = 40 * time.Millisecond

	r.Start()
	defer r.Stop()

	// Without backoff, a millisecond-paced loop would run hundreds of attempts
	// in this window; with it, the first four take 10+20+40+40ms.
	time.Sleep(100 * time.Millisecond)
	if got := a.registerCount(httpID); got > 8 {
		t.Errorf("register attempts = %d in 100ms, want a backed-off handful", got)
	}
	if got := a.registerCount(httpID); got < 2 {
		t.Errorf("register attempts = %d in 100ms, want the loop to keep retrying", got)
	}
}
