# Lambo Architecture

Lambo is a high-availability, score-based load balancing proxy for RPC endpoints. It pools unreliable backend services, maintains dynamic reliability scores for each, and uses a weighted selection algorithm to distribute traffic.

## 1. System Overview

The system operates across three concurrent layers:

1.  **Request Layer**: Handles incoming HTTP requests and routes them to optimal backends.
2.  **Management Layer**: Background processes that monitor health and update reliability scores.
3.  **Data Layer**: Thread-safe storage for endpoint state and metrics.

## 2. Component Design

### 2.1 Data Layer
**Location**: [`pkg/manager/manager.go`](pkg/manager/manager.go)

The Data Layer maintains the state of all backend endpoints. It is designed to be thread-safe for concurrent access by the Request and Management layers.

*   **Endpoint**: Represents a single backend service. It tracks:
    *   `Address` & `URL`: Connection details.
    *   `IsHealthy`: Current health status (true/false).
    *   `Score`: Dynamic reliability score (0.0 - 1.0).
    *   `LatencyMs`: Most recent request duration.
    *   `ConsecutiveFails`: Counter for health check failures.
*   **EndpointPool**: A container for all `Endpoint` objects, protected by a `sync.RWMutex`.

### 2.2 Management Layer
**Location**: [`pkg/manager/manager.go`](pkg/manager/manager.go)

This layer runs maintenance tasks independently of client traffic.

*   **Health Checker**:
    *   Runs in a dedicated goroutine.
    *   Periodically pings `[scheme]://[address]/health`.
    *   Marks endpoints as unhealthy after `HealthCheckFailures` consecutive failures.
    *   Resets the score of recovered endpoints to a baseline (0.5).
*   **Score Tracker** (via `UpdateScore`):
    *   Updates reliability scores based on request outcomes (success/failure) and latency.
    *   Uses an **Exponentially Weighted Moving Average (EWMA)** to smooth score updates over time.

### 2.3 Request Layer
**Location**: [`pkg/proxy/proxy.go`](pkg/proxy/proxy.go) & [`pkg/manager/manager.go`](pkg/manager/manager.go)

*   **Load Balancer** (`EndpointPool.Select`):
    *   Filters for healthy endpoints.
    *   Calculates an "Effective Weight" for each endpoint based on its Score and Latency.
    *   Selects a target using a **Weighted Random Choice** algorithm.
*   **Proxy Server** (`ProxyHandler`):
    *   Accepts incoming HTTP requests.
    *   Delegates selection to the Load Balancer.
    *   Uses `httputil.ReverseProxy` to forward requests.
    *   Interlopes response processing to feed performance metrics back to the Score Tracker.

## 3. Core Algorithms

### 3.1 Reliability Scoring (EWMA)
The score ($S$) is updated after every request using an Exponentially Weighted Moving Average to prioritize recent performance.

Formula:
$$S_{new} = \alpha \cdot P_{i} + (1 - \alpha) \cdot S_{old}$$

*   $\alpha$: Decay factor (configured via `ewma_alpha`, default 0.1).
*   $P_i$: Instantaneous performance (1.0 for success, 0.0 for failure).
*   $S_{old}$: Previous score.

### 3.2 Endpoint Selection (Weighted Random Choice)
Traffic is distributed probabilistically to prevent "thundering herd" issues on the best endpoint while favoring better performers.

1.  **Filter**: Only endpoints where `IsHealthy == true` are candidates.
2.  **Effective Weight Calculation**:
    $$W_{eff} = S \cdot \frac{1}{\log_{2}(\text{LatencyMs} + 2)}$$
    *   $S$: Current Score.
    *   Latency Penalty: High latency significantly reduces the selection probability, even for high-scoring nodes.
3.  **Selection**: A random value $R$ is chosen between 0 and total weight. The algorithm iterates through candidates, subtracting weights from $R$ until $R \le 0$.

## 4. Configuration
**Location**: [`pkg/config/config.go`](pkg/config/config.go)

Configuration is loaded from YAML files or environment variables.

| Parameter | Env Var | Default | Description |
|-----------|---------|---------|-------------|
| `proxy_port` | `PROXY_PORT` | 8080 | Port for the proxy server. |
| `health_check_interval` | `HEALTH_CHECK_INTERVAL` | 5s | Frequency of background health checks. |
| `health_check_failures` | `HEALTH_CHECK_FAILURES` | 3 | Failures before marking unhealthy. |
| `ewma_alpha` | `EWMA_ALPHA` | 0.1 | Smoothing factor for score updates. |
| `backend_addresses` | `BACKEND_ADDRESSES` | (list) | List of target RPC endpoints. |

## 5. Project Layout

```
lambo/
├── cmd/
│   └── lambo/       # Main application entry point
├── pkg/
│   ├── config/      # Configuration loading and validation
│   ├── manager/     # Endpoint pool, health checking, and load balancing logic
│   └── proxy/       # HTTP server and reverse proxy implementation
├── config.yaml      # Default configuration file
└── README.md        # Project documentation
```

