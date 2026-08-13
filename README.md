# My Update Center (MUC)

A distributed system monitoring tool that tracks pending package updates across your home network. The system consists of client that run on each machine and a server that aggregates and displays update information via a web interface.

## Overview

This project monitors system updates across multiple machines in your network:
- **Client**: Runs on each machine (macOS, Linux) and periodically checks for pending updates
- **Server**: Aggregates update information from all clients and provides a web dashboard
- **Communication**: Uses NATS for messaging between clients and server

## Supported Systems

### Package Managers
- **Linux**: apt (Debian/Ubuntu), dnf (Fedora/RHEL), yum (older RHEL/CentOS), nixos-rebuild (NixOS)
- **macOS**: Homebrew (brew)

### Platforms
- macOS (darwin)
- Linux (ARM64, x86_64)

## Architecture

- **Clients** connect to a NATS server and publish system information including pending updates every minute
- **Server** can run an embedded NATS server (default) or connect to an external NATS instance
- **Storage** uses BoltDB to persist system state
- **Web Interface** provides a real-time dashboard to view all systems and their update status

## Building

### Build All Components
```bash
make build
```

### Build Individual Components
```bash
make client    # Build client only
make server    # Build server only
```

### Cross-Compilation
```bash
make linux     # Build Linux binaries for all architectures (ARM64, AMD64)
make linux-all # Build both client and server for all Linux architectures
```

### Other Targets
```bash
make help      # Show all available make targets
make clean     # Clean build artifacts
make test      # Run tests
make fmt       # Format code
```

## Configuration

### Server Configuration

The server can be configured via multiple sources, loaded in the following priority order (highest to lowest):

1. **Environment variables** (prefixed with `MUC_`)
2. **`.env` file** in the working directory
3. **`config.yml`** in the working directory or `/etc/muc/`
4. **Built-in defaults**

Configuration is powered by [Viper](https://github.com/spf13/viper).

**Configuration Keys:**

| Key | Env Var | Default | Description |
|-----|---------|---------|-------------|
| `nats_url` | `MUC_NATS_URL` | `embedded` | NATS server URL (`embedded` runs a built-in NATS server) |
| `nats_port` | `MUC_NATS_PORT` | `4222` | Port for embedded NATS server |
| `db_path` | `MUC_DB_PATH` | `systems.db` | Path to BoltDB database file |
| `http_port` | `MUC_HTTP_PORT` | `8080` | Web server port |
| `consul_url` | `MUC_CONSUL_URL` | `http://localhost:8500` | Consul agent URL for service registration |
| `consul_tags` | `MUC_CONSUL_TAGS` | (none) | Comma-separated Consul tags for the HTTP (`muc`) service |
| `consul_nats_tags` | `MUC_CONSUL_NATS_TAGS` | (falls back to `consul_tags`) | Comma-separated Consul tags for the NATS (`muc-nats`) service |

**CLI Flags:**
- `--dev`: Enable dev mode (debug logging enabled)
- `--json`: Output logs in JSON format (default: text format)

**Example using environment variables:**
```bash
export MUC_HTTP_PORT=3000
export MUC_DB_PATH=/var/lib/muc/systems.db
export MUC_CONSUL_TAGS=production,us-east-1
./muc-server --dev
```

**Example `.env` file:**
```
MUC_HTTP_PORT=3000
MUC_DB_PATH=/var/lib/muc/systems.db
MUC_CONSUL_URL=http://consul.local:8500
MUC_CONSUL_TAGS=production,us-east-1
```

**Example `config.yml`:**
```yaml
nats_url: embedded
nats_port: 4222
db_path: /var/lib/muc/systems.db
http_port: 8080
consul_url: http://consul.local:8500
consul_tags:
  - production
  - us-east-1
# Optional: tag the muc-nats service differently. Omit to reuse consul_tags.
consul_nats_tags:
  - production
  - nats
```

### Client Configuration

The client supports automatic server discovery using multiple methods, tried in order:

1. **Environment Variable Override**: `MUC_NATS_URL` (highest priority)
2. **DNS SRV Records**: Looks for `_muc-server._tcp`, `_muc-nats._tcp`, or `_nats._tcp` service records (tried in order)
3. **Consul Service Discovery**: Queries Consul for `nats`, `muc-nats`, or `muc-server` services (tried in order, most generic first)
4. **Environment Variable Fallback**: `MUC_NATS_SERVER_IP` with default port 4222
5. **Hardcoded Default**: `192.168.1.157:4222` (last resort)

**Environment Variables:**
- `MUC_NATS_URL`: NATS server URL (e.g., `nats://192.168.1.157:4222`) - **explicit override, highest priority**
- `MUC_NATS_SERVER_IP`: NATS server IP address (fallback if discovery fails)
- `MUC_NATS_PORT`: NATS server port (default: `4222`)
- `MUC_NATS_DISCOVERY_DOMAIN`: Domain for DNS SRV lookup (default: tries hostname domain, `local`, `lan`, `home.arpa`)
- `MUC_NATS_DISCOVERY_SERVICE`: Service name for DNS SRV lookup (default: tries `muc-server`, `muc-nats`, `nats` in order)
- `MUC_CONSUL_HTTP_ADDR`: Consul API address (default: `localhost:8500`)
- `MUC_NATS_CONSUL_SERVICE`: Consul service name to query (default: tries `nats`, `muc-nats`, `muc-server` in order)

**CLI Flags:**
- `--dev`: Enable dev mode (debug logging enabled)
- `--json`: Output logs in JSON format (default: text format)

**Examples:**

Explicit configuration:
```bash
export MUC_NATS_URL=nats://192.168.1.157:4222
./muc-client --dev
```

DNS SRV record discovery (requires DNS configuration):
```bash
# Configure DNS SRV records (tried in order):
# - _muc-server._tcp.example.com (most specific, tried first)
# - _muc-nats._tcp.example.com
# - _nats._tcp.example.com (generic, tried last)
# Example: _muc-server._tcp.example.com -> server.example.com:4222
export MUC_NATS_DISCOVERY_DOMAIN=example.com
./muc-client

# Or specify a specific service name:
export MUC_NATS_DISCOVERY_DOMAIN=example.com
export MUC_NATS_DISCOVERY_SERVICE=muc-server
./muc-client
```

Consul service discovery:
```bash
# Ensure Consul is running and service is registered
# Service names tried in order: nats, muc-nats, muc-server
export MUC_CONSUL_HTTP_ADDR=consul.example.com:8500
export MUC_NATS_CONSUL_SERVICE=nats  # Optional: specify a specific service name
./muc-client
```

Automatic discovery (no configuration needed if DNS/Consul is set up):
```bash
./muc-client  # Will try DNS SRV, then Consul, then fallback to default
```

### Logging

The application uses structured logging with `log/slog`:

- **Default**: Info level, text format (syslog-like) on stdout
- **Dev Mode** (`--dev`): Debug level enabled, shows detailed debug information
- **JSON Output** (`--json`): Outputs logs in JSON format for log aggregation systems

Examples:
```bash
# Default: Info level, text format
./muc-server

# Dev mode: Debug level, text format
./muc-server --dev

# JSON output
./muc-server --json

# Dev mode with JSON output
./muc-server --dev --json
```

### Running as a Service

The client can be run as a systemd service. See `client/contrib/systemd.unit` for an example systemd unit file.

The packages install `muc-client.service` plus a `muc-client-recheck.path` unit
that watches `/var/lib/rpm` and `/var/lib/dpkg`. After any package transaction it
reloads the client, which re-checks for updates once things settle — so the
dashboard reflects a `dnf upgrade` within seconds instead of at the next poll
(every 5 minutes). The client still polls if the path unit is unavailable.

### Keeping update data fresh

The client runs as **root** and keeps no state of its own — it writes nothing to
disk, owns no system user, and has no `StateDirectory`. Its whole job is asking
the system package manager what is pending, and every package manager it is
packaged for needs root to answer usefully:

- **apt and zypper** cannot refresh their repository metadata at all without
  root, so an unprivileged check silently under-reports.

- **dnf and yum** *can* refresh unprivileged — but into a per-user cache that
  your own `sudo dnf` never reads, and the client pins a shorter expiry (1h) than
  stock repos use for interactive work (6h). Between those two marks the client
  has re-synced and your shell has not, so the dashboard correctly lists an
  update that `sudo dnf update` reports as "nothing to do". Running as root puts
  both on `/var/cache/dnf`, and the client's hourly refresh then warms the very
  cache your shell reads — so the two cannot drift apart, and your interactive
  `dnf update` gets more accurate as a side effect.

  If you ever see the dashboard claim updates your shell denies, compare using
  `dnf check-update --refresh` — without `--refresh`, dnf answers from a cache it
  still considers valid. A client running unprivileged logs a warning saying
  exactly this.

`/var/lib/muc` belongs to **muc-server** alone, which runs as the unprivileged
`muc` user and keeps `systems.db` there. The client deliberately does not share
it: `StateDirectory=` makes systemd re-chown the directory to the unit's user on
every start, recursively, so a root client sharing it would take the directory
away from the server — which then keeps working until its next restart, because
permission is checked at `open()` rather than per write. Upgrading the client
package repairs the ownership on hosts where an older version already did this.

One more consequence is worth knowing:

- **Repository signing keys are accepted unattended.** A repo with
  `repo_gpgcheck=1` verifies `repomd.xml` against a GPG keyring under the cache
  dir, separate from the system rpm keyring. The client passes `-y` so it adopts
  those keys without prompting — otherwise the prompt is declined and
  `skip_if_unavailable` drops the repo along with all of its updates.

  The trade-off is deliberate: the client trusts whatever key the repo's
  configured `gpgkey=` URL serves. `check-update` installs nothing, so this only
  affects which keys are trusted for *metadata* verification; package
  installation still verifies against the root-owned rpm keyring. Note that with
  the client on the shared `/var/cache/dnf`, that keyring is the same one your own
  `dnf` consults, rather than a muc-owned copy. Every import is logged, so you can
  audit what a host has trusted:

  ```bash
  journalctl -u muc-client | grep "imported repository signing keys"
  ```

  If a repo still cannot be read for another reason (network, a mirror outage),
  the client reports it rather than hiding it: the host shows "Status unknown" (or
  a ⚠ next to its update badge) with the skipped repo named in the expanded row.

**Last Seen** is the dashboard's one timestamp: the server stamps it when it
receives a check-in, so it means the host is reachable. The client also reports
when it actually queried its package manager (`updates_checked_at`, available
over the API), but the dashboard does not show it as a second time — two
timestamps in two places mostly serve to disagree with each other. Instead, when
the two diverge the host's update badge is flagged with a ⚠, so a badge backed
by hours-old data is not mistaken for a fresh one.

## Usage

1. **Start the server**:
   ```bash
   cd server
   make run
   # Or with dev mode for debug logging:
   ./muc-server --dev
   ```
   The web interface will be available at `http://localhost:8080` (or your configured MUC_HTTP_PORT).

2. **Run clients on each machine**:
   ```bash
   cd client
   ./muc-client
   # Or with dev mode for debug logging:
   ./muc-client --dev
   ```
   The client will automatically connect to the NATS server and start reporting system information.

3. **View the dashboard**: Open your browser to the server's HTTP port to see all systems and their update status.

## Web Interface

The web dashboard provides:
- Overview of all monitored systems
- System details (hostname, OS, architecture, IP address)
- Pending update lists with package names and versions
- Sortable columns
- Expandable rows to view detailed update information
- Last seen timestamps, plus when the update data itself was collected
- Warnings when an update check was incomplete (e.g. a repository was skipped)
- Tailnet status for hosts that use Tailscale (see below)

### Tailnet status

Hosts that are on a [Tailscale](https://tailscale.com) tailnet get a dot ahead of
their hostname: green when the host is on a tailnet right now, grey when it
belongs to one but is not connected. Hovering the dot names the tailnet, which is
the point of the indicator — a host can belong to several tailnets but can only
be joined to one at a time. The expanded row spells the same thing out as a
**Tailnet** field. The dot sits in a fixed-width gutter, so hostnames stay
aligned whether or not a given host has one.

Nothing needs to be configured. The client asks tailscaled directly over its
local API socket (`/run/tailscale/tailscaled.sock`), which needs no CLI on the
host at all. Where the socket is not where the client looks, notably on macOS,
it falls back to `tailscale status --json`, and failing that to looking for a
tailnet address on a Tailscale interface — which proves the host is connected
but cannot name the tailnet.

Finding the CLI is its own hunt, because there is no one place it lives: `PATH`
first, then the usual install locations (`/usr/bin`, `/usr/local/bin`,
`/opt/homebrew/bin`, the macOS app bundle, and so on), and as a last resort
whatever `systemctl cat tailscaled.service` — or `tailscale.service` — says
tailscaled was started from. That covers both a unit that names the daemon
directly, where the CLI is its neighbour, and one that runs it through an
environment wrapper such as `flox activate -d <dir> -- tailscaled`, where the
directory being activated leads to the same bin directory. If none of that finds
it, the client gives up on the CLI and uses the sources above.

Hosts that have never been seen on a tailnet report nothing at all and show no
dot, so a fleet that does not use Tailscale never sees this feature. The reverse
is sticky: once a host has been seen on a tailnet the server remembers which one,
so a host that drops off — or whose client stops reporting Tailscale entirely —
shows a grey dot naming the tailnet it was last on, rather than silently losing
its indicator.

## Alternatives

Instead of using this tool, you could run a cron job or systemd timer to auto-update. However, this approach has drawbacks:
- Sometimes reboots are required after updates
- Services (like Docker) may crash during updates
- You lose visibility and control over when updates are applied

This tool gives you visibility into pending updates across all your systems, allowing you to plan updates appropriately.

## License

This project is licensed under the Apache License. See the [LICENSE](LICENSE) file for details.
