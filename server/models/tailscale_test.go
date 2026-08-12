package models

import (
	"testing"
	"time"
)

var mergeNow = time.Date(2026, 8, 12, 15, 4, 5, 0, time.UTC)

func TestMergeTailscaleNeverSeenStaysNil(t *testing.T) {
	// A host that has never reported a tailnet keeps no record at all, which is
	// what keeps the indicator off hosts that do not use Tailscale.
	if got := MergeTailscale(nil, nil, mergeNow); got != nil {
		t.Errorf("MergeTailscale(nil, nil) = %+v, want nil", got)
	}
}

func TestMergeTailscaleFirstConnectedReport(t *testing.T) {
	incoming := &Tailscale{Connected: true, Tailnet: "example.com", MagicDNSSuffix: "tail2ad946.ts.net", IP: "100.64.0.5", State: "Running"}

	got := MergeTailscale(nil, incoming, mergeNow)
	if got == nil {
		t.Fatal("MergeTailscale returned nil for a connected report")
	}
	if got.LastTailnet != "example.com" {
		t.Errorf("LastTailnet = %q, want %q", got.LastTailnet, "example.com")
	}
	if got.LastConnectedAt != "2026-08-12T15:04:05Z" {
		t.Errorf("LastConnectedAt = %q, want %q", got.LastConnectedAt, "2026-08-12T15:04:05Z")
	}
	if incoming.LastTailnet != "" || incoming.LastConnectedAt != "" {
		t.Error("MergeTailscale mutated the incoming report")
	}
}

func TestMergeTailscaleDisconnectedKeepsLastTailnet(t *testing.T) {
	previous := &Tailscale{Connected: true, Tailnet: "example.com", State: "Running", LastTailnet: "example.com", LastConnectedAt: "2026-08-12T14:00:00Z"}
	// A stopped daemon no longer knows its tailnet, so the name has to come
	// from what the server remembers.
	incoming := &Tailscale{Connected: false, State: "Stopped"}

	got := MergeTailscale(previous, incoming, mergeNow)
	if got.Connected {
		t.Error("Connected = true, want false")
	}
	if got.LastTailnet != "example.com" {
		t.Errorf("LastTailnet = %q, want %q", got.LastTailnet, "example.com")
	}
	if got.LastConnectedAt != "2026-08-12T14:00:00Z" {
		t.Errorf("LastConnectedAt = %q, want the previous timestamp", got.LastConnectedAt)
	}
}

func TestMergeTailscaleSwitchedTailnets(t *testing.T) {
	// A host can belong to several tailnets but join only one at a time; the
	// remembered tailnet must follow the switch.
	previous := &Tailscale{Connected: true, Tailnet: "example.com", LastTailnet: "example.com", LastConnectedAt: "2026-08-12T14:00:00Z"}
	incoming := &Tailscale{Connected: true, Tailnet: "other.org", MagicDNSSuffix: "tailabc123.ts.net", State: "Running"}

	got := MergeTailscale(previous, incoming, mergeNow)
	if got.Tailnet != "other.org" {
		t.Errorf("Tailnet = %q, want %q", got.Tailnet, "other.org")
	}
	if got.LastTailnet != "other.org" {
		t.Errorf("LastTailnet = %q, want %q", got.LastTailnet, "other.org")
	}
	if got.LastConnectedAt != "2026-08-12T15:04:05Z" {
		t.Errorf("LastConnectedAt = %q, want the merge timestamp", got.LastConnectedAt)
	}
}

func TestMergeTailscaleClientStopsReporting(t *testing.T) {
	// An older client — or one that lost sight of tailscaled — sends no
	// tailscale field. The host stays visible as a known, disconnected node.
	previous := &Tailscale{Connected: true, Tailnet: "example.com", IP: "100.64.0.5", State: "Running", LastTailnet: "example.com", LastConnectedAt: "2026-08-12T14:00:00Z"}

	got := MergeTailscale(previous, nil, mergeNow)
	if got == nil {
		t.Fatal("MergeTailscale dropped a host that was previously on a tailnet")
	}
	if got.Connected {
		t.Error("Connected = true, want false")
	}
	if got.State != StateUnreported {
		t.Errorf("State = %q, want %q", got.State, StateUnreported)
	}
	if got.LastTailnet != "example.com" {
		t.Errorf("LastTailnet = %q, want %q", got.LastTailnet, "example.com")
	}
	if got.IP != "" {
		t.Errorf("IP = %q, want it cleared — the address is no longer current", got.IP)
	}
}

func TestMergeTailscaleFallsBackToMagicDNSSuffix(t *testing.T) {
	// Detected from the interface alone, or from a status without a tailnet
	// name: the MagicDNS suffix is the only handle on the tailnet.
	incoming := &Tailscale{Connected: true, MagicDNSSuffix: "tail2ad946.ts.net", State: "Running"}

	got := MergeTailscale(nil, incoming, mergeNow)
	if got.LastTailnet != "tail2ad946.ts.net" {
		t.Errorf("LastTailnet = %q, want %q", got.LastTailnet, "tail2ad946.ts.net")
	}
}

func TestMergeTailscaleUnnamedConnectionKeepsRememberedName(t *testing.T) {
	// The interface fallback proves connectivity but cannot name the tailnet;
	// it must not erase the name the server already learned.
	previous := &Tailscale{Connected: true, Tailnet: "example.com", LastTailnet: "example.com", LastConnectedAt: "2026-08-12T14:00:00Z"}
	incoming := &Tailscale{Connected: true, IP: "100.64.0.5", State: "Running"}

	got := MergeTailscale(previous, incoming, mergeNow)
	if got.LastTailnet != "example.com" {
		t.Errorf("LastTailnet = %q, want %q", got.LastTailnet, "example.com")
	}
}
