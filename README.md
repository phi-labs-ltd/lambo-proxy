# Go Proxy Architecture Proposal: Score-Based Load Balancing

This document outlines the architecture for a highly available and reliable Go proxy that pools unreliable RPC endpoints, maintains a dynamic reliability score for each, and uses a score-based algorithm to distribute incoming client requests.

## 1. Core Architecture Overview

The system is split into three main, concurrently operating layers: the Request Layer, the Management Layer, and the Data Layer.

### Layer Breakdown

| Layer | Components | Primary Function |
|-------|------------|------------------|
| Request Layer | ProxyServer, LoadBalancer | Accepts client requests and intelligently routes them to the best-performing backend based on the current score. |
| Management Layer | HealthChecker, ScoreTracker | Runs continuously in the background, monitoring backend status (latency, errors) and dynamically updating the reliability scores. |
| Data Layer | EndpointPool, Endpoint | The thread-safe state container for all backend services and their associated dynamic metrics. |

## 2. Component Design

### 2.1. The Data Layer (EndpointPool and Endpoint)

The EndpointPool will be the central, thread-safe repository for all backend information, protected by a synchronization primitive (e.g., sync.RWMutex).

**Endpoint Struct:**

```go
type Endpoint struct {
    Address     string         // e.g., "localhost:8081"
    IsHealthy   bool           // Status from HealthChecker
    Score       float64        // Reliability Score (0.0 to 1.0)
    LatencyMs   float64        // Latest measured latency
    Mutex       sync.Mutex     // Protects metrics updates
}
```

**EndpointPool Struct:**

```go
type EndpointPool struct {
    Endpoints []*Endpoint
    Mutex     sync.RWMutex // Protects the slice itself
}
```

### 2.2. The Management Layer

This layer uses Go routines to run maintenance tasks independently of client traffic.

#### A. Health Checker

The HealthChecker periodically pings a dedicated endpoint (e.g., /health) on each backend.

- **Mechanism**: Runs in a dedicated goroutine every N seconds.
- **Action**: If a backend fails M consecutive checks, Endpoint.IsHealthy is set to false. If it succeeds, IsHealthy is set to true.

#### B. Score Tracker (Reliability Scoring)

This is the core logic for measuring reliability. We will use an Exponentially Weighted Moving Average (EWMA) algorithm to calculate the Score. This allows recent performance to have a greater impact than old performance, making the system highly reactive.

- **Metric**: The score should be inversely related to the observed Error Rate and Latency.

**Updating the Score**: When a request returns:

- **Success**: Increase the score towards 1.0.
- **Failure/Timeout**: Decrease the score towards 0.0.
- **Latency**: Use latency as a dampening factor; higher latency slightly reduces the effective score.

**Proposed Score Update Formula:**

For an endpoint $i$, the new score $S_{new}$ is calculated:

$$S_{new} = \alpha \cdot P_{i} + (1 - \alpha) \cdot S_{old}$$

Where:

- $S_{old}$ is the current score.
- $\alpha$ is the decay factor (e.g., 0.1, determines responsiveness).
- $P_i$ is the instantaneous performance (1.0 for success, 0.0 for failure).

**Latency Penalty**: The selection algorithm (in the Load Balancer) handles latency by making high-latency endpoints less likely to be chosen, even if they have a perfect success score.

### 2.3. The Request Layer

#### A. Load Balancer (LoadBalancer)

The load balancer uses a Weighted Random Choice (WRC) algorithm, where the weight is derived from the current reliability score. This prevents a single high-score endpoint from getting 100% of the traffic, ensuring others remain active and tested.

**Algorithm Steps:**

1. **Filter**: Get all endpoints where IsHealthy is true. If no endpoints are healthy, the request fails or is queued (depending on policy).

2. **Calculate Effective Weight**: For each healthy endpoint, calculate its effective weight $W_{eff}$:

   $$W_{eff} = S \cdot \frac{1}{\log_{2}(\text{LatencyMs} + 2)}$$

   - $S$: The current Endpoint.Score (0.0 to 1.0).
   - The logarithmic term is a simple dampening factor: low latency (~0ms) gives a multiplier of $\approx 1$, while high latency (e.g., 62ms) gives a multiplier of $\approx 0.16$, significantly reducing its effective weight.

3. **Weighted Random Selection**:
   - Sum all $W_{eff}$ to get $W_{total}$.
   - Generate a random number $R$ between $0$ and $W_{total}$.
   - Iterate through the endpoints, subtracting $W_{eff}$ from $R$ until $R \le 0$. The current endpoint is the chosen one.

#### B. Proxy Server (ProxyServer)

The main entry point. It receives an HTTP request and delegates the selection and request forwarding to the LoadBalancer.

**Sequence for a Client Request:**

1. Client connects to ProxyServer.
2. ProxyServer calls LoadBalancer.Select() to get the optimal Endpoint.
3. ProxyServer uses a Go reverse proxy (httputil.ReverseProxy or similar) to forward the request.

**Critical Step**: Upon receiving the response (or failure/timeout):

- The request duration and success status are reported back to the ScoreTracker.
- The ScoreTracker updates the Endpoint.Score concurrently.

## 3. Implementation Policy

| Policy Area | Action/Logic |
|-------------|--------------|
| Recovery | If a backend is marked IsHealthy: false, the HealthChecker continues to probe it. Once healthy, its Score is reset to a baseline (e.g., 0.5) to give it a chance to prove reliability again. |
| Timeout | The proxy must have a strict timeout (e.g., 5 seconds). If the RPC request hits this timeout, it is treated as a failure and severely penalizes the endpoint's score. |
| Concurrency | All components (HealthChecker, LoadBalancer, ScoreTracker, and the main server's request handlers) must operate concurrently, accessing the EndpointPool only through thread-safe methods (using sync.Mutex or sync.RWMutex). |
