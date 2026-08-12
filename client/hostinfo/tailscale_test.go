package hostinfo

import "testing"

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
