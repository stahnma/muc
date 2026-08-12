package hostinfo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// tailscaleSockets are the paths tailscaled listens on for its local API. The
// socket is world-accessible on Linux, so the service user can read status from
// it without the CLI being installed anywhere it can reach.
var tailscaleSockets = []string{
	"/var/run/tailscale/tailscaled.sock",
	"/run/tailscale/tailscaled.sock",
}

// tailscaleBinaries are the install locations to try when the CLI is not on
// PATH. A service unit's PATH is minimal, and on macOS the CLI lives inside the
// app bundle rather than in a bin dir.
var tailscaleBinaries = []string{
	"/usr/bin/tailscale",
	"/usr/local/bin/tailscale",
	"/usr/sbin/tailscale",
	"/bin/tailscale",
	"/opt/homebrew/bin/tailscale",
	"/snap/bin/tailscale",
	"/Applications/Tailscale.app/Contents/MacOS/Tailscale",
}

// tailscaleUnits are the systemd units that run tailscaled, in the order to
// consult them. Distribution packages use tailscaled.service; hand-rolled units
// tend to be named after the product.
var tailscaleUnits = []string{"tailscaled.service", "tailscale.service"}

// tailscaleInterfacePrefixes are the network interface names Tailscale creates:
// tailscale0 on Linux, utunN on macOS, ts* on some BSDs.
var tailscaleInterfacePrefixes = []string{"tailscale", "utun", "ts"}

// cgnatRange is 100.64.0.0/10, the shared-address space Tailscale assigns node
// addresses from.
var cgnatRange = &net.IPNet{IP: net.IPv4(100, 64, 0, 0).To4(), Mask: net.CIDRMask(10, 32)}

// TailscaleStatus reports the host's tailnet membership, or nil if the host has
// no tailnet identity to report.
//
// Three sources are tried in order of how much they can tell us:
//
//  1. tailscaled's local API socket. This is the primary source because it
//     needs nothing installed for the client to reach — the CLI is often
//     absent from any path an unprivileged, ProtectHome=true service can see
//     (a nix, flox, or Homebrew install, say), and it is exactly those hosts
//     that would otherwise go unnamed.
//  2. `tailscale status --json`, for hosts where the socket is not where we
//     look for it — notably macOS. See findTailscaleBinary for how the CLI is
//     located, which is its own small hunt.
//  3. A tailnet address on a Tailscale interface. This proves the host is
//     connected but cannot say to which tailnet, so it is a last resort:
//     better a dot with no name than no dot at all.
func TailscaleStatus() *TailscaleInfo {
	if info := tailscaleFromLocalAPI(); info != nil {
		return info
	}
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

// tailscaleFromLocalAPI asks tailscaled directly over its unix socket. It
// returns the same payload the CLI prints, without needing the CLI.
func tailscaleFromLocalAPI() *TailscaleInfo {
	for _, socket := range tailscaleSockets {
		if _, err := os.Stat(socket); err != nil {
			continue
		}
		data, err := queryTailscaleLocalAPI(socket)
		if err != nil {
			slog.Debug("Failed to query tailscaled local API", "socket", socket, "error", err)
			continue
		}
		if info := parseTailscaleStatus(data); info != nil {
			return info
		}
	}
	return nil
}

func queryTailscaleLocalAPI(socket string) ([]byte, error) {
	client := &http.Client{
		Timeout: tailscaleStatusTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var dialer net.Dialer
				return dialer.DialContext(ctx, "unix", socket)
			},
		},
	}

	// The host in the URL is a fixed name tailscaled expects rather than a real
	// one; the connection goes to the socket dialled above.
	req, err := http.NewRequest(http.MethodGet, "http://local-tailscaled.sock/localapi/v0/status", nil)
	if err != nil {
		return nil, err
	}
	// Marks this as a deliberate local API call, which newer tailscaled builds
	// require before they will answer.
	req.Header.Set("Sec-Tailscale", "localapi")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tailscaled local API returned %s", resp.Status)
	}
	// A tailnet with many peers makes for a large status; cap what we read so a
	// misbehaving daemon cannot balloon the client's memory.
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// findTailscaleBinary locates the tailscale CLI: PATH first, then the usual
// install locations, then — as a last resort — wherever the systemd unit that
// runs tailscaled says it lives. There is no single right answer to find: the
// CLI ships in /usr/bin from a distribution package, in /opt/homebrew on macOS,
// and out of a nix or flox tree on hosts that install it that way.
func findTailscaleBinary() string {
	if path, err := exec.LookPath("tailscale"); err == nil {
		return path
	}
	for _, path := range tailscaleBinaries {
		if isExecutableFile(path) {
			return path
		}
	}
	return tailscaleBinaryFromSystemd()
}

// tailscaleBinaryFromSystemd asks systemd where tailscaled was started from and
// looks for the CLI alongside it.
func tailscaleBinaryFromSystemd() string {
	if runtime.GOOS != "linux" {
		return ""
	}
	for _, unit := range tailscaleUnits {
		ctx, cancel := context.WithTimeout(context.Background(), tailscaleStatusTimeout)
		out, err := exec.CommandContext(ctx, "systemctl", "cat", unit).Output()
		cancel()
		if err != nil {
			slog.Debug("Failed to read systemd unit for Tailscale", "unit", unit, "error", err)
			continue
		}
		if path := tailscaleBinaryFromUnit(string(out)); path != "" {
			slog.Debug("Located tailscale CLI from systemd unit", "unit", unit, "path", path)
			return path
		}
	}
	return ""
}

// tailscaleBinaryFromUnit finds the CLI from the text of a systemd unit,
// covering the two shapes an ExecStart takes in practice:
//
//	ExecStart=/usr/sbin/tailscaled --state=...
//	ExecStart=/usr/bin/flox activate -d /root/tailscale-home -- tailscaled ...
//
// The first names the daemon binary, and the CLI is its neighbour. The second
// runs the daemon through an environment wrapper, so no path to it appears on
// the command line at all — but the directory being activated leads to the
// same bin directory.
func tailscaleBinaryFromUnit(unit string) string {
	for _, line := range strings.Split(unit, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ExecStart") {
			continue
		}
		_, command, found := strings.Cut(line, "=")
		if !found {
			continue
		}

		fields := strings.Fields(command)
		for _, field := range fields {
			// systemd allows prefix characters such as "-@+!" on the executable.
			field = strings.TrimLeft(field, "-@+!:")
			if !strings.HasPrefix(field, "/") {
				continue
			}
			if base := filepath.Base(field); base != "tailscaled" && base != "tailscale" {
				continue
			}
			if path := siblingTailscale(field); path != "" {
				return path
			}
		}

		if path := floxTailscale(fields); path != "" {
			return path
		}
	}
	return ""
}

// siblingTailscale returns the tailscale CLI sitting next to the given binary.
func siblingTailscale(binary string) string {
	candidate := filepath.Join(filepath.Dir(binary), "tailscale")
	if isExecutableFile(candidate) {
		return candidate
	}
	return ""
}

// floxTailscale handles `flox activate -d <dir> -- tailscaled`, where the
// binaries live in the activated environment's run directory rather than
// anywhere named on the command line. The run directory carries the system
// name (".flox/run/x86_64-linux.tailscale-root-dev/bin"), so it is matched by
// glob rather than spelled out.
func floxTailscale(fields []string) string {
	for i, field := range fields {
		if field != "-d" && field != "--dir" {
			continue
		}
		if i+1 >= len(fields) || !strings.HasPrefix(fields[i+1], "/") {
			continue
		}
		matches, err := filepath.Glob(filepath.Join(fields[i+1], ".flox", "run", "*", "bin", "tailscale"))
		if err != nil {
			continue
		}
		for _, match := range matches {
			if isExecutableFile(match) {
				return match
			}
		}
	}
	return ""
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
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
