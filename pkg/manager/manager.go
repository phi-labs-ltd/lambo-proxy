package manager

import (
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/archway-network/lambo/pkg/config"
)

// --- Data Layer Structures (2.1) ---

// Endpoint holds the dynamic metrics for a single backend service.
type Endpoint struct {
	Address          string     // e.g., "localhost:8081"
	URL              *url.URL   // Parsed URL for the reverse proxy
	IsHealthy        bool       // Status from HealthChecker
	Score            float64    // Reliability Score (0.0 to 1.0)
	LatencyMs        float64    // Latest measured latency
	Mutex            sync.Mutex // Protects metrics updates
	ConsecutiveFails int        // Counter for HealthChecker
}

// EndpointPool is the thread-safe container for all backend services.
type EndpointPool struct {
	Endpoints []*Endpoint
	Mutex     sync.RWMutex // Protects the slice itself
}

// NewEndpoint creates a new endpoint, parsing the address into a URL object.
// Supports multiple URL formats:
//   - host:port
//   - http://host:port
//   - https://host:port
//   - host:port/rpc
//   - http://host:port/rpc
//   - https://host:port/rpc
func NewEndpoint(addr string) *Endpoint {
	var u *url.URL
	var err error

	// Try parsing as a full URL first
	u, err = url.Parse(addr)
	if err != nil || u.Scheme == "" {
		// No scheme provided, determine scheme based on port
		scheme := "http"
		if len(addr) > 4 && addr[len(addr)-4:] == ":443" {
			scheme = "https"
		}
		// Parse with prepended scheme
		u, err = url.Parse(fmt.Sprintf("%s://%s", scheme, addr))
		if err != nil {
			// Fallback: create a minimal URL
			u, _ = url.Parse(fmt.Sprintf("http://%s", addr))
		}
	}

	// Normalize the URL: ensure path starts with / if present
	if u.Path != "" && u.Path[0] != '/' {
		u.Path = "/" + u.Path
	}

	// Store the original address for logging/display purposes
	displayAddr := addr
	if u.Host != "" {
		displayAddr = u.Host
		if u.Path != "" {
			displayAddr = u.Host + u.Path
		}
	}

	return &Endpoint{
		Address:   displayAddr,
		URL:       u,
		IsHealthy: true, // Assume healthy on startup
		Score:     0.5,  // Start at baseline score
		LatencyMs: 50.0, // Baseline latency
	}
}

// --- Management Layer (2.2) ---

// UpdateScore applies the Exponentially Weighted Moving Average (EWMA) to the endpoint's score.
// Latency penalty is handled separately in the Load Balancer selection.
func (e *Endpoint) UpdateScore(success bool, duration time.Duration, ewmaAlpha float64) {
	e.Mutex.Lock()
	defer e.Mutex.Unlock()

	// 1. Update Latency
	e.LatencyMs = float64(duration.Milliseconds())

	// 2. Determine Instantaneous Performance (Pi)
	var pi float64
	if success {
		pi = 1.0
	} else {
		pi = 0.0
	}

	// 3. Apply EWMA Formula: S_new = alpha * P_i + (1 - alpha) * S_old
	e.Score = ewmaAlpha*pi + (1-ewmaAlpha)*e.Score

	// Ensure score remains within [0.0, 1.0]
	if e.Score < 0.01 {
		e.Score = 0.01 // Prevent zero weight, ensuring test traffic continues
	} else if e.Score > 1.0 {
		e.Score = 1.0
	}

	log.Printf("[ScoreTracker] %s | Latency: %.2fms | Success: %t | New Score: %.3f",
		e.Address, e.LatencyMs, success, e.Score)
}

// HealthChecker continuously probes backends and updates their IsHealthy status.
func HealthChecker(p *EndpointPool, cfg *config.Config) {
	for {
		p.Mutex.RLock()
		endpoints := p.Endpoints
		p.Mutex.RUnlock()

		var wg sync.WaitGroup
		for _, ep := range endpoints {
			wg.Add(1)
			go func(ep *Endpoint) {
				defer wg.Done()
				checkBackendHealth(ep, cfg)
			}(ep)
		}
		wg.Wait()
		time.Sleep(cfg.HealthCheckInterval)
	}
}

func checkBackendHealth(ep *Endpoint, cfg *config.Config) {
	// Mock Health Check: Send a simple GET request
	client := http.Client{Timeout: 3 * time.Second}
	// Construct health check URL using the endpoint's base URL and append /health
	baseURL := ep.URL
	healthPath := "/health"
	
	// If endpoint has a base path, append /health to it
	if baseURL.Path != "" && baseURL.Path != "/" {
		// Ensure base path ends with / before appending health
		if baseURL.Path[len(baseURL.Path)-1] == '/' {
			healthPath = baseURL.Path + "health"
		} else {
			healthPath = baseURL.Path + "/health"
		}
	}
	
	// Build the full health check URL
	healthURL := fmt.Sprintf("%s://%s%s", baseURL.Scheme, baseURL.Host, healthPath)
	resp, err := client.Get(healthURL)

	ep.Mutex.Lock()
	defer ep.Mutex.Unlock()

	if err != nil || resp.StatusCode != http.StatusOK {
		ep.ConsecutiveFails++
		log.Printf("[HealthCheck] %s FAILED (%d/%d): %v", ep.Address, ep.ConsecutiveFails, cfg.HealthCheckFailures, err)

		if ep.ConsecutiveFails >= cfg.HealthCheckFailures && ep.IsHealthy {
			ep.IsHealthy = false
			log.Printf("[HealthCheck] %s marked UNHEALTHY (Policy: Failure Count)", ep.Address)
		}
	} else {
		if !ep.IsHealthy {
			// Recovery Policy: Reset score to baseline (0.5) when healthy again
			ep.Score = 0.5
			log.Printf("[HealthCheck] %s recovered. Score reset to 0.5.", ep.Address)
		}
		ep.IsHealthy = true
		ep.ConsecutiveFails = 0
	}
	if resp != nil {
		resp.Body.Close()
	}
}

// --- Request Layer (2.3) ---

// Select implements the Weighted Random Choice (WRC) algorithm.
func (p *EndpointPool) Select() *Endpoint {
	p.Mutex.RLock()
	defer p.Mutex.RUnlock()

	// 1. Filter: Get all healthy endpoints
	var candidates []*Endpoint
	for _, ep := range p.Endpoints {
		ep.Mutex.Lock()
		if ep.IsHealthy {
			candidates = append(candidates, ep)
		}
		ep.Mutex.Unlock()
	}

	if len(candidates) == 0 {
		return nil // No healthy endpoint found
	}

	// 2. Calculate Effective Weight (Weff)
	var effectiveWeights []float64
	var totalWeight float64

	for _, ep := range candidates {
		ep.Mutex.Lock() // Lock to read metrics
		score := ep.Score
		latency := ep.LatencyMs
		ep.Mutex.Unlock() // Unlock after reading

		// Latency is at least 1ms to prevent log(1) which is zero
		if latency < 1.0 {
			latency = 1.0
		}

		// Calculate Latency Penalty Multiplier: 1 / log2(LatencyMs + 2)
		// log2(1+2) = 1.58 -> multiplier ~0.63
		// log2(50+2) = 5.70 -> multiplier ~0.17
		latencyMultiplier := 1.0 / math.Log2(latency+2)

		// W_eff = S * Multiplier
		weight := score * latencyMultiplier
		effectiveWeights = append(effectiveWeights, weight)
		totalWeight += weight
	}

	// 3. Weighted Random Selection
	r := rand.Float64() * totalWeight
	var runningWeight float64
	for i, weight := range effectiveWeights {
		runningWeight += weight
		if r <= runningWeight {
			return candidates[i]
		}
	}
	// Should not be reached, but as a safe fallback
	return candidates[len(candidates)-1]
}

