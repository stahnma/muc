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
- Linux (ARM, ARM64, x86_64)

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
make linux     # Build Linux binaries for all architectures (ARM, ARM64, AMD64)
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
| `consul_tags` | `MUC_CONSUL_TAGS` | (none) | Comma-separated Consul service tags |

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
- Last seen timestamps

## Alternatives

Instead of using this tool, you could run a cron job or systemd timer to auto-update. However, this approach has drawbacks:
- Sometimes reboots are required after updates
- Services (like Docker) may crash during updates
- You lose visibility and control over when updates are applied

This tool gives you visibility into pending updates across all your systems, allowing you to plan updates appropriately.

## License

This project is licensed under the Apache License. See the [LICENSE](LICENSE) file for details.
