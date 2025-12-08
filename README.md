# Lambo

Lambo is a high-availability, score-based load balancing proxy for RPC endpoints. It pools unreliable backend services, maintains dynamic reliability scores for each, and uses a weighted selection algorithm to distribute incoming client requests to the best-performing nodes.

## How to Run

### Prerequisites
- Go 1.21 or higher

### Running the Proxy

You can run the proxy directly using `go run`:

```bash
go run cmd/lambo/main.go
```

By default, it looks for a configuration file at `./config.yaml`. You can specify a custom path using the `-config` flag:

```bash
go run cmd/lambo/main.go -config /path/to/config.yaml
```

### Configuration

Configuration can be provided via a YAML file or environment variables. Environment variables take precedence.

| Parameter | Env Var | Default |
|-----------|---------|---------|
| `proxy_port` | `PROXY_PORT` | `8080` |
| `health_check_interval` | `HEALTH_CHECK_INTERVAL` | `5s` |
| `ewma_alpha` | `EWMA_ALPHA` | `0.1` |
| `backend_addresses` | `BACKEND_ADDRESSES` | `...` |

See `config.yaml` for an example configuration.

## Architecture

For detailed information on the system architecture, component design, and scoring algorithms, please refer to [Architecture.md](Architecture.md).
