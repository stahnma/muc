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
| `remote_updates` | `MUC_REMOTE_UPDATES` | `false` | Allow the dashboard to run updates on hosts that have opted in (see [Running updates from the dashboard](#running-updates-from-the-dashboard)) |

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
# Off by default. See "Running updates from the dashboard".
remote_updates: false
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
- `MUC_ALLOW_REMOTE_UPDATES`: Let the dashboard run updates on this host (default: `false`)
- `MUC_UPDATE_COMMAND`: The update script to run (default: the packaged `/usr/libexec/muc/upd`)

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
- A check-in button on every host, to refresh a row now rather than at its next poll (see [Asking a host to check in](#asking-a-host-to-check-in))
- An update button for hosts that have opted in (see [Running updates from the dashboard](#running-updates-from-the-dashboard))

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

## Asking a host to check in

A client checks in every five minutes, and out of band whenever the package
database changes underneath it (see [Keeping update data fresh](#keeping-update-data-fresh)).
When you want an answer sooner than either — you have just patched a host by
hand, or a row looks wrong and you want to know whether it still is — expand the
host's row and press **🔄 Check in now**. The host reads its package manager and
publishes the result, and the row updates when it arrives.

Unlike the update button this is on for every host, with nothing to enable at
either end. A check-in installs nothing and changes nothing: it publishes exactly
what the client publishes on its own every few minutes, so there is no state for
the button to put a host into that time would not have put it into anyway.

What it does cost is a package-manager query, and on dnf that means talking to
every configured repository. So the client refuses a second command within **10
seconds** of the last one it accepted, and says how long to wait — enough that
anything on the network that can reach NATS still cannot hold a host at a
continuous metadata refresh, and short enough that pressing the button twice
because you doubt the first answer is not an argument.

The request is answered when the host accepts it, not when the check has run: a
cold `dnf check-update --refresh` against a slow mirror can take longer than
anyone will hold an HTTP request open for. The button therefore waits for the
check-in itself to arrive rather than for its own response, and gives up after
two minutes if nothing does. A host that is offline — or running a client from
before this existed — answers nothing at all, and the dashboard says so.

The same thing over the API:

```bash
curl -X POST http://muc-server:8080/api/systems/web01/checkin
```

## Running updates from the dashboard

The dashboard can ask a host to install its pending updates: expand the host's
row and press **⬇️ Run updates now**. The host runs `upd` — the same script you
would run in its shell, shipped with the client package at
`/usr/libexec/muc/upd` — which picks the package manager from what is installed
rather than from the distro name.

**It is off by default, and both ends have to agree before anything can happen:**

| Where | Setting | Effect |
|-------|---------|--------|
| Each host (`/etc/muc/client.yml`) | `allow_remote_updates: true` | The client subscribes to its update-command subject. Without it the command reaches nobody. |
| The server (`/etc/muc/config.yml`) | `remote_updates: true` | The dashboard draws the button and `POST /api/systems/{hostname}/update` works. Without it the route returns 403. |

The host's opt-in is the one that actually gates anything. A client that has not
set `allow_remote_updates` never subscribes, so no server configuration — and no
one poking the API by hand — can make it install a thing. That is why the opt-in
lives with the host being patched rather than only on the server: the machine
that takes the risk is the machine that consents to it.

**This is a convenience for a trusted network, not an authorization boundary.**
Neither the dashboard nor NATS authenticates anyone, so on a host that has opted
in, anything that can reach the NATS port can trigger an update. That is an
acceptable trade on a home LAN and is not one anywhere else.

Enable it on a host:

```yaml
# /etc/muc/client.yml
allow_remote_updates: true
# Optional; defaults to the packaged /usr/libexec/muc/upd, then
# /usr/local/bin/upd, /usr/bin/upd, then 'upd' on PATH.
# update_command: /usr/local/bin/upd
```

```bash
systemctl restart muc-client
```

A host that opts in but has no usable update script logs the reason and does not
advertise the capability, so the dashboard shows no button for it rather than
one that always fails.

### What happens during a run

The request is answered as soon as the host accepts it, and the run reports back
separately, so the dashboard shows progress as it goes: **⏳ Update running**
next to the host's update badge, and in the expanded row a **Command output**
pane that fills in live as the package manager works. One run at a time per host
— a second request while one is in progress is refused with "an update is
already running on this host". When the run finishes the client immediately
re-reads the package manager and checks in again — the update command has
already exited, so there is nothing to wait for — and the host's badge drops to
**Up to date** as soon as that check completes, rather than still listing the
packages that were just installed until the next poll.

### Where the update actually runs

Two paths, chosen per host rather than assumed:

**With systemd, as root**, the client starts the command in a named transient
unit (`systemd-run --unit=muc-update-<id> --wait`) rather than forking it. Two
reasons, both of which otherwise look like a mysteriously half-finished upgrade:

- The transaction may upgrade `muc-client` itself, whose scriptlet restarts the
  unit — and a restart kills everything in the unit's control group, including
  the `dnf` running the transaction. `setsid` does not help: a forked child in
  its own session still dies, because the cgroup is what is being killed.
- `muc-client.service` is sandboxed, and a forked child inherits all of it:
  `ProtectSystem=full` (a read-only `/usr` no package manager can install into),
  `PrivateTmp`, `ProtectHome`, and `NoNewPrivileges` — which also blocks the
  SELinux transitions rpm scriptlets may expect. A transient unit starts in a
  context close to what `sudo dnf update` would have given it.

**Everywhere else** — a host without systemd, a container, macOS, an
unprivileged client, or a systemd that refuses the transient unit — the client
forks the command directly and reads its output from pipes. Streaming, the exit
status, and the run record all behave the same; the difference is that the
update inherits whatever environment the client has.

That path is not left fragile: `muc-client.service` sets `KillMode=process`, so
systemd signals only the client and an update it forked runs to completion even
if a transaction restarts the unit underneath it. On a host with no systemd
there is no such hazard to begin with.

Either way, a run whose client was restarted mid-flight completes on the host but
never reports its result. The dashboard shows such a
run as **❓ Update result unknown** after 45 minutes rather than claiming it is
still going, and the next check-in shows what actually got installed.

Runs are bounded at 30 minutes.

### Live output, and what is kept

Output is streamed as it is produced, coalesced into a chunk every 400 ms (or
sooner once 16 KB has piled up) rather than sent line by line — an upgrade emits
hundreds of short lines, and one message each would be a message storm nobody
could read anyway. The pane opens by itself while a run is going and on a
failure, where the output is the answer; after a success it collapses, since it
is mostly package names.

Two different things hold that output, and they are worth telling apart:

The unit's output goes to the journal, and the client follows it there
(`journalctl --follow _SYSTEMD_UNIT=muc-update-<id>.service`) to produce the live
stream. The obvious alternative, `systemd-run --pipe`, hands the client's own
file descriptors to the unit — but passing them travels over D-Bus, and on an
SELinux system that message is refused for a service in `unconfined_service_t`:
the bus drops the connection and `systemd-run` reports `Failed to start
transient service unit: Connection reset by peer` **without running anything at
all**. Rocky 10 does exactly this. Going through the journal asks nothing of the
bus beyond starting the unit, and leaves the full log on the host as a side
effect.

- **The live buffer** is in memory on the server, 64 KB per host, for the run in
  flight. It is what a page opened or reloaded mid-run is seeded from, through
  `GET /api/systems/{hostname}/update/output`. It is not persisted: a server
  restart forgets it, which is the right trade for a running commentary.
- **The saved tail** is the last 8 KB, stored with the system when the run ends.
  That is what survives a reload once the run is over, and what the dashboard
  shows for a run it did not watch.

A browser that watched a run keeps the fuller live text on screen after it
finishes, so nothing shrinks under you; reload and you get the 8 KB tail.

Streaming stops after 2 MB in one run, on the theory that anything past that is a
repository serving something strange rather than output worth reading. The run
still finishes and its tail still arrives. Chunks are numbered, and the server
drops them rather than queueing without limit if a browser cannot keep up — the
pane says `[… some live output was dropped …]` where that happened rather than
splicing two unrelated moments together.

### When a run fails before it starts

`Failed to start transient service unit: ...` in the output pane means systemd
would not create the unit, so **the update command never ran and nothing was
installed** — the exit status belongs to `systemd-run`, not to the package
manager.

One instance of this is fixed rather than diagnosed: `Connection reset by peer`
was `--pipe`'s file-descriptor passing being refused by SELinux, and the client
no longer uses `--pipe` (see above). If some other reason turns up, the client
probes before each run: when a transient unit cannot be started it says so at
the top of the output, runs the command directly, and warns when the sandbox
will defeat that too (a direct run under `ProtectSystem=full` cannot write
`/usr`). To find out why systemd refused, on the affected host:

```bash
# does it work at all, from a root shell?
sudo systemd-run --quiet --pipe --wait --collect /bin/true && echo ok

# what confinement is the client actually running under?
systemctl show muc-client -p ProtectSystem -p NoNewPrivileges -p PrivateTmp -p SELinuxContext

# what did the client and systemd say at the time?
journalctl -u muc-client -n 50 --no-pager
sudo ausearch -m avc -ts recent | tail -20   # SELinux denials, if any
```

If the root shell works and the client does not, the client's confinement is
what is blocking it. If neither works, it is the host's systemd or D-Bus. Where
transient units cannot be made to work, let the direct run install packages
with an override:

```bash
sudo systemctl edit muc-client
# [Service]
# ProtectSystem=false
```

That trades the sandbox for a working update, and leaves the other hazard in
place: a transaction that upgrades `muc-client` restarts the unit and kills the
package manager with it, since both are then in the same control group.

The complete, untruncated output stays on the host, in the journal, under the
unit the run used:

```bash
journalctl -u muc-update-<id>            # <id> is shown in the dashboard's run record
journalctl -u 'muc-update-*' --since -1d # every run of the last day
```

`--collect` removes the unit once it has finished, but its journal entries are
records and outlive it.

## Alternatives

Instead of using this tool, you could run a cron job or systemd timer to auto-update. However, this approach has drawbacks:
- Sometimes reboots are required after updates
- Services (like Docker) may crash during updates
- You lose visibility and control over when updates are applied

This tool gives you visibility into pending updates across all your systems, allowing you to plan updates appropriately.

## License

This project is licensed under the Apache License. See the [LICENSE](LICENSE) file for details.
