package models

import "time"

// Tailscale describes a host's tailnet membership.
//
// The first five fields are what the client reports at check-in. The last two
// are the server's own memory of the host: a node that drops off its tailnet
// stops being able to say which tailnet that was, and a client too old to
// report tailnets says nothing at all — in both cases the dashboard should
// still show a known host as disconnected rather than losing the indicator.
type Tailscale struct {
	// Connected is whether the host was on a tailnet at its last check-in.
	Connected bool `json:"connected"`
	// Tailnet is the human-readable tailnet name, when known.
	Tailnet string `json:"tailnet,omitempty"`
	// MagicDNSSuffix identifies the tailnet when Tailnet is unavailable, and
	// tells two tailnets apart for a host that belongs to several.
	MagicDNSSuffix string `json:"magic_dns_suffix,omitempty"`
	// IP is the host's IPv4 address on the tailnet.
	IP string `json:"ip,omitempty"`
	// State is tailscaled's backend state ("Running", "Stopped",
	// "NeedsLogin", ...), or StateUnreported when the host has gone quiet
	// about Tailscale altogether.
	State string `json:"state,omitempty"`

	// LastTailnet is the most recent tailnet this host was seen on. Server-set.
	LastTailnet string `json:"last_tailnet,omitempty"`
	// LastConnectedAt is when it was last seen connected, RFC3339 UTC.
	// Server-set.
	LastConnectedAt string `json:"last_connected_at,omitempty"`
}

// StateUnreported marks a host that was known to be on a tailnet but has since
// stopped reporting any Tailscale status — an old client, a removed Tailscale
// install, or a daemon the client can no longer see.
const StateUnreported = "unreported"

// Name is the best available name for the tailnet in this report.
func (t *Tailscale) Name() string {
	if t == nil {
		return ""
	}
	if t.Tailnet != "" {
		return t.Tailnet
	}
	return t.MagicDNSSuffix
}

// knownTailnet is the best available name for the tailnet this host belongs to,
// whether or not it is connected right now.
func (t *Tailscale) knownTailnet() string {
	if t == nil {
		return ""
	}
	if t.LastTailnet != "" {
		return t.LastTailnet
	}
	return t.Name()
}

// MergeTailscale folds an incoming client report into what the server already
// knew about a host, and returns the record to store.
//
// Membership is sticky by design: once a host has been seen on a tailnet, it
// keeps an entry — disconnected rather than absent — so the dashboard does not
// flicker between "has an indicator" and "has none" every time a node drops off
// its tailnet or a client loses its view of tailscaled.
func MergeTailscale(previous, incoming *Tailscale, now time.Time) *Tailscale {
	if incoming == nil {
		if previous == nil {
			// Never seen on a tailnet: stay silent, so hosts that do not use
			// Tailscale show nothing at all.
			return nil
		}
		return &Tailscale{
			Connected:       false,
			State:           StateUnreported,
			LastTailnet:     previous.knownTailnet(),
			LastConnectedAt: previous.LastConnectedAt,
		}
	}

	merged := *incoming
	if previous != nil {
		merged.LastTailnet = previous.knownTailnet()
		merged.LastConnectedAt = previous.LastConnectedAt
	}
	if incoming.Connected {
		if name := incoming.Name(); name != "" {
			merged.LastTailnet = name
		}
		merged.LastConnectedAt = now.UTC().Format(time.RFC3339)
	}
	return &merged
}
