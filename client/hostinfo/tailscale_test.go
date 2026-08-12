package hostinfo

import (
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestQueryTailscaleLocalAPI covers the source that matters most in practice:
// hosts where the CLI is installed somewhere the service user cannot reach (a
// nix, flox or Homebrew tree, or behind ProtectHome=true) still get a named
// tailnet, because the daemon's socket answers without any CLI at all.
func TestQueryTailscaleLocalAPI(t *testing.T) {
	// macOS caps unix socket paths at ~104 bytes and t.TempDir() is long, so
	// keep the name short.
	socket := filepath.Join(t.TempDir(), "s.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	defer listener.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/localapi/v0/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Sec-Tailscale") != "localapi" {
			t.Errorf("Sec-Tailscale header = %q, want %q", r.Header.Get("Sec-Tailscale"), "localapi")
		}
		w.Write([]byte(`{"BackendState":"Running","HaveNodeKey":true,
			"TailscaleIPs":["100.126.136.109"],
			"CurrentTailnet":{"Name":"example.com","MagicDNSSuffix":"tail2ad946.ts.net"}}`))
	})
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()

	data, err := queryTailscaleLocalAPI(socket)
	if err != nil {
		t.Fatalf("queryTailscaleLocalAPI: %v", err)
	}
	info := parseTailscaleStatus(data)
	if info == nil {
		t.Fatal("parseTailscaleStatus returned nil for the local API response")
	}
	if !info.Connected || info.Tailnet != "example.com" {
		t.Errorf("got %+v, want connected on example.com", info)
	}
}

func TestQueryTailscaleLocalAPIErrors(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "s.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("unix sockets unavailable: %v", err)
	}
	defer listener.Close()

	// A daemon that answers with anything but 200 must not be mistaken for a
	// host with no tailnet — the caller falls through to its other sources.
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no", http.StatusForbidden)
	})}
	go server.Serve(listener)
	defer server.Close()

	if _, err := queryTailscaleLocalAPI(socket); err == nil {
		t.Error("queryTailscaleLocalAPI returned no error for a 403 response")
	}
	if _, err := queryTailscaleLocalAPI(filepath.Join(t.TempDir(), "missing.sock")); err == nil {
		t.Error("queryTailscaleLocalAPI returned no error for a missing socket")
	}
}

func TestParseTailscaleStatusRunning(t *testing.T) {
	data := []byte(`{
		"BackendState": "Running",
		"HaveNodeKey": true,
		"TailscaleIPs": ["100.126.136.109", "fd7a:115c:a1e0::273b:886d"],
		"MagicDNSSuffix": "tail2ad946.ts.net",
		"CurrentTailnet": {
			"Name": "mastahnke@gmail.com",
			"MagicDNSSuffix": "tail2ad946.ts.net",
			"MagicDNSEnabled": true
		},
		"Self": {"DNSName": "smallboi.tail2ad946.ts.net.", "TailscaleIPs": ["100.126.136.109"]}
	}`)

	info := parseTailscaleStatus(data)
	if info == nil {
		t.Fatal("parseTailscaleStatus returned nil for a running node")
	}
	if !info.Connected {
		t.Error("Connected = false, want true for BackendState Running")
	}
	if info.Tailnet != "mastahnke@gmail.com" {
		t.Errorf("Tailnet = %q, want %q", info.Tailnet, "mastahnke@gmail.com")
	}
	if info.MagicDNSSuffix != "tail2ad946.ts.net" {
		t.Errorf("MagicDNSSuffix = %q, want %q", info.MagicDNSSuffix, "tail2ad946.ts.net")
	}
	// The IPv6 address is present too; the IPv4 one is what we report.
	if info.IP != "100.126.136.109" {
		t.Errorf("IP = %q, want %q", info.IP, "100.126.136.109")
	}
	if info.State != "Running" {
		t.Errorf("State = %q, want %q", info.State, "Running")
	}
}

func TestParseTailscaleStatusStopped(t *testing.T) {
	// `tailscale down` drops CurrentTailnet but keeps the node's identity, so
	// the tailnet is still nameable from the node's own MagicDNS name.
	data := []byte(`{
		"BackendState": "Stopped",
		"HaveNodeKey": true,
		"Self": {"DNSName": "smallboi.tail2ad946.ts.net.", "TailscaleIPs": ["100.126.136.109"]}
	}`)

	info := parseTailscaleStatus(data)
	if info == nil {
		t.Fatal("parseTailscaleStatus returned nil for a stopped node that has joined a tailnet")
	}
	if info.Connected {
		t.Error("Connected = true, want false for BackendState Stopped")
	}
	if info.MagicDNSSuffix != "tail2ad946.ts.net" {
		t.Errorf("MagicDNSSuffix = %q, want %q", info.MagicDNSSuffix, "tail2ad946.ts.net")
	}
	if info.State != "Stopped" {
		t.Errorf("State = %q, want %q", info.State, "Stopped")
	}
}

func TestParseTailscaleStatusNeverJoined(t *testing.T) {
	// Tailscale installed but never logged in: nothing to report, so the host
	// stays free of any indicator rather than showing a permanent grey dot.
	cases := map[string]string{
		"needs login": `{"BackendState": "NeedsLogin", "HaveNodeKey": false, "Self": {"DNSName": ""}}`,
		"no state":    `{"BackendState": "NoState", "HaveNodeKey": false}`,
		"malformed":   `not json at all`,
		"empty":       ``,
	}
	for name, data := range cases {
		if info := parseTailscaleStatus([]byte(data)); info != nil {
			t.Errorf("%s: parseTailscaleStatus = %+v, want nil", name, info)
		}
	}
}

func TestParseTailscaleStatusLoggedOutWithNodeKey(t *testing.T) {
	// Logged out but still keyed: the host has been on a tailnet, so it should
	// report itself as disconnected rather than disappearing.
	info := parseTailscaleStatus([]byte(`{"BackendState": "NeedsLogin", "HaveNodeKey": true}`))
	if info == nil {
		t.Fatal("parseTailscaleStatus returned nil for a keyed but logged-out node")
	}
	if info.Connected {
		t.Error("Connected = true, want false for BackendState NeedsLogin")
	}
	if info.State != "NeedsLogin" {
		t.Errorf("State = %q, want %q", info.State, "NeedsLogin")
	}
}

// writeExecutable creates a stand-in binary at path so the discovery code has
// something to find.
func writeExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// TestTailscaleBinaryFromUnitSibling covers a distribution-style unit, where
// ExecStart names the daemon and the CLI is its neighbour.
func TestTailscaleBinaryFromUnitSibling(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "tailscale"))

	unit := "[Service]\nExecStart=" + filepath.Join(dir, "tailscaled") +
		" --state=/var/lib/tailscale/tailscaled.state --port=41641\n"

	if got := tailscaleBinaryFromUnit(unit); got != filepath.Join(dir, "tailscale") {
		t.Errorf("tailscaleBinaryFromUnit = %q, want %q", got, filepath.Join(dir, "tailscale"))
	}
}

// TestTailscaleBinaryFromUnitExecPrefix checks systemd's executable prefixes
// ("-" to ignore failures, "@", "+", "!") do not hide the path behind them.
func TestTailscaleBinaryFromUnitExecPrefix(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, filepath.Join(dir, "tailscale"))

	unit := "[Service]\nExecStart=-" + filepath.Join(dir, "tailscaled") + " --state=x\n"

	if got := tailscaleBinaryFromUnit(unit); got != filepath.Join(dir, "tailscale") {
		t.Errorf("tailscaleBinaryFromUnit = %q, want %q", got, filepath.Join(dir, "tailscale"))
	}
}

// TestTailscaleBinaryFromUnitFlox covers a unit that runs tailscaled through an
// environment wrapper. No path to the daemon appears on the command line at
// all — only the directory being activated, which leads to the bin directory.
func TestTailscaleBinaryFromUnitFlox(t *testing.T) {
	home := t.TempDir()
	want := filepath.Join(home, ".flox", "run", "x86_64-linux.tailscale-root-dev", "bin", "tailscale")
	writeExecutable(t, want)

	unit := "[Unit]\nDescription=Tailscale VPN Service (via Flox)\n[Service]\n" +
		"ExecStart=/usr/bin/flox activate -d " + home + " -- tailscaled --state=" + home + "/tailscaled.state\n"

	if got := tailscaleBinaryFromUnit(unit); got != want {
		t.Errorf("tailscaleBinaryFromUnit = %q, want %q", got, want)
	}
}

// TestTailscaleBinaryFromUnitGivesUp checks the search fails quietly rather
// than returning a path that is not there — detection then falls through to
// the interface check instead of shelling out to nothing.
func TestTailscaleBinaryFromUnitGivesUp(t *testing.T) {
	cases := map[string]string{
		"no ExecStart":     "[Unit]\nDescription=Tailscale\n",
		"nothing on disk":  "[Service]\nExecStart=/nowhere/at/all/tailscaled --state=x\n",
		"unrelated binary": "[Service]\nExecStart=/usr/bin/env FOO=1\n",
		"flox dir missing": "[Service]\nExecStart=/usr/bin/flox activate -d /nowhere -- tailscaled\n",
		"empty":            "",
	}
	for name, unit := range cases {
		if got := tailscaleBinaryFromUnit(unit); got != "" {
			t.Errorf("%s: tailscaleBinaryFromUnit = %q, want \"\"", name, got)
		}
	}
}

func TestMagicDNSSuffix(t *testing.T) {
	cases := map[string]string{
		"smallboi.tail2ad946.ts.net.": "tail2ad946.ts.net",
		"smallboi.tail2ad946.ts.net":  "tail2ad946.ts.net",
		"smallboi":                    "",
		"":                            "",
	}
	for in, want := range cases {
		if got := magicDNSSuffix(in); got != want {
			t.Errorf("magicDNSSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFirstIPv4(t *testing.T) {
	cases := []struct {
		name string
		ips  []string
		want string
	}{
		{"ipv6 first", []string{"fd7a:115c:a1e0::273b:886d", "100.126.136.109"}, "100.126.136.109"},
		{"ipv6 only", []string{"fd7a:115c:a1e0::273b:886d"}, ""},
		{"none", nil, ""},
		{"garbage", []string{"not-an-ip"}, ""},
	}
	for _, c := range cases {
		if got := firstIPv4(c.ips); got != c.want {
			t.Errorf("%s: firstIPv4(%v) = %q, want %q", c.name, c.ips, got, c.want)
		}
	}
}

func TestIsTailscaleInterface(t *testing.T) {
	for _, name := range []string{"tailscale0", "utun4", "ts0", "Tailscale"} {
		if !isTailscaleInterface(name) {
			t.Errorf("isTailscaleInterface(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"eth0", "wlan0", "docker0", "lo", "wg0"} {
		if isTailscaleInterface(name) {
			t.Errorf("isTailscaleInterface(%q) = true, want false", name)
		}
	}
}
