package hostinfo

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// TailscaleInfo describes the host's tailnet membership at check-in time.
//
// A nil *TailscaleInfo means "this host has nothing to say about tailnets" —
// Tailscale is not installed, or its daemon has never joined one. The client
// then omits the field from its check-in entirely, so hosts that do not
// participate in a tailnet never grow an indicator in the dashboard. Detection
// is zero-config: nothing needs to be enabled for a host that is on a tailnet
// to start reporting it.
type TailscaleInfo struct {
	// Connected reports whether the host is on a tailnet right now.
	Connected bool `json:"connected"`
	// Tailnet is the human-readable tailnet name, e.g. "example.com" or the
	// owning account. Empty when it could not be determined.
	Tailnet string `json:"tailnet,omitempty"`
	// MagicDNSSuffix (e.g. "tail2ad946.ts.net") identifies the tailnet even
	// when Tailnet is unavailable, and distinguishes tailnets for a host that
	// belongs to several but can only be joined to one at a time.
	MagicDNSSuffix string `json:"magic_dns_suffix,omitempty"`
	// IP is the host's IPv4 address on the tailnet.
	IP string `json:"ip,omitempty"`
	// State is tailscaled's own backend state ("Running", "Stopped",
	// "NeedsLogin", ...). It is what explains a grey dot in the UI without
	// anyone having to read the client's logs.
	State string `json:"state,omitempty"`
}

// tailscaleStatusTimeout bounds the status query. tailscaled answers from local
// state, so this only trips when the daemon is wedged — and a check-in must not
// be held up by it.
const tailscaleStatusTimeout = 5 * time.Second

// tailscaleBinaries are the install locations to try when the CLI is not on
// PATH. The client runs as an unprivileged service user whose PATH is minimal,
// and on macOS the CLI lives inside the app bundle rather than in a bin dir.
var tailscaleBinaries = []string{
	"/usr/bin/tailscale",
	"/usr/local/bin/tailscale",
	"/opt/homebrew/bin/tailscale",
	"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
}

// tailscaleInterfacePrefixes are the network interface names Tailscale creates:
// tailscale0 on Linux, utunN on macOS, ts* on some BSDs.
var tailscaleInterfacePrefixes = []string{"tailscale", "utun", "ts"}

// cgnatRange is 100.64.0.0/10, the shared-address space Tailscale assigns node
// addresses from.
var cgnatRange = &net.IPNet{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)}

// TailscaleStatus reports the host's tailnet membership, or nil if the host has
// no tailnet identity to report.
//
// The authoritative source is `tailscale status --json`, which is the only way
// to learn *which* tailnet the host is on. Where that is unavailable — the CLI
// is not installed where we look, or the local API socket is not readable by
// the service user — a tailnet-addressed interface still proves the host is
// connected, so fall back to that and report membership without a name rather
// than reporting nothing.
func TailscaleStatus() *TailscaleInfo {
	if bin := findTailscaleBinary(); bin != "" {
		out, err := runTailscaleStatus(bin)
		if err != nil {
			slog.Debug("Failed to query tailscale status", "binary", bin, "error", err)
		} else if info := parseTailscaleStatus(out); info != nil {
			return info
		}
	}
	return tailscaleFromInterfaces()
}

func findTailscaleBinary() string {
	if path, err := exec.LookPath("tailscale"); err == nil {
		return path
	}
	for _, path := range tailscaleBinaries {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

func runTailscaleStatus(bin string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), tailscaleStatusTimeout)
	defer cancel()

	// Output() captures stdout only, which keeps the CLI's version-skew warning
	// on stderr out of the JSON.
	return exec.CommandContext(ctx, bin, "status", "--json").Output()
}

// parseTailscaleStatus turns `tailscale status --json` output into a report, or
// returns nil when the host has never joined a tailnet.
func parseTailscaleStatus(data []byte) *TailscaleInfo {
	var raw struct {
		BackendState   string   `json:"BackendState"`
		HaveNodeKey    bool     `json:"HaveNodeKey"`
		TailscaleIPs   []string `json:"TailscaleIPs"`
		MagicDNSSuffix string   `json:"MagicDNSSuffix"`
		CurrentTailnet *struct {
			Name           string `json:"Name"`
			MagicDNSSuffix string `json:"MagicDNSSuffix"`
		} `json:"CurrentTailnet"`
		Self *struct {
			DNSName      string   `json:"DNSName"`
			TailscaleIPs []string `json:"TailscaleIPs"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		slog.Debug("Failed to parse tailscale status output", "error", err)
		return nil
	}

	info := &TailscaleInfo{
		// "Running" is the only backend state in which the node is actually on
		// the tailnet; Stopped, NeedsLogin and Starting all mean it is not.
		Connected: raw.BackendState == "Running",
		State:     raw.BackendState,
	}
	if raw.CurrentTailnet != nil {
		info.Tailnet = raw.CurrentTailnet.Name
		info.MagicDNSSuffix = raw.CurrentTailnet.MagicDNSSuffix
	}
	if info.MagicDNSSuffix == "" {
		info.MagicDNSSuffix = raw.MagicDNSSuffix
	}
	if info.MagicDNSSuffix == "" && raw.Self != nil {
		// A logged-out node loses CurrentTailnet but keeps its own DNS name,
		// e.g. "host.tail2ad946.ts.net." — the suffix still names the tailnet.
		info.MagicDNSSuffix = magicDNSSuffix(raw.Self.DNSName)
	}

	ips := raw.TailscaleIPs
	if len(ips) == 0 && raw.Self != nil {
		ips = raw.Self.TailscaleIPs
	}
	info.IP = firstIPv4(ips)

	// Only speak up for a host that has actually joined a tailnet at some
	// point. A daemon that is installed but has never been logged in has no
	// membership to report, and reporting it would pin a permanent grey dot on
	// hosts that merely have the package installed.
	if !info.Connected && !raw.HaveNodeKey && info.Tailnet == "" && info.MagicDNSSuffix == "" {
		return nil
	}
	return info
}

// magicDNSSuffix extracts the tailnet suffix from a node's MagicDNS name:
// "host.tail2ad946.ts.net." -> "tail2ad946.ts.net".
func magicDNSSuffix(dnsName string) string {
	name := strings.TrimSuffix(strings.TrimSpace(dnsName), ".")
	if _, suffix, found := strings.Cut(name, "."); found {
		return suffix
	}
	return ""
}

func firstIPv4(ips []string) string {
	for _, raw := range ips {
		if ip := net.ParseIP(strings.TrimSpace(raw)); ip != nil && ip.To4() != nil {
			return ip.To4().String()
		}
	}
	return ""
}

// tailscaleFromInterfaces detects tailnet membership from the network stack
// alone. It proves the host is connected but cannot say to which tailnet, so it
// is only a fallback for when the status query is unavailable.
func tailscaleFromInterfaces() *TailscaleInfo {
	ifaces, err := net.Interfaces()
	if err != nil {
		slog.Debug("Failed to list network interfaces for Tailscale detection", "error", err)
		return nil
	}

	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || !isTailscaleInterface(iface.Name) {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipNet.IP.To4()
			// The CGNAT check matters: utunN on macOS is used by other VPNs
			// too, and only Tailscale addresses come out of 100.64.0.0/10.
			if ip4 == nil || !cgnatRange.Contains(ip4) {
				continue
			}
			slog.Debug("Detected Tailscale from interface address", "interface", iface.Name, "ip", ip4.String())
			return &TailscaleInfo{Connected: true, IP: ip4.String(), State: "Running"}
		}
	}
	return nil
}

func isTailscaleInterface(name string) bool {
	lower := strings.ToLower(name)
	for _, prefix := range tailscaleInterfacePrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	return false
}
