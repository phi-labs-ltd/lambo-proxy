package main

import (
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/archway-network/lambo/config"
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
func NewEndpoint(addr string) *Endpoint {
	// Determine scheme based on port: 443 = HTTPS, others = HTTP
	scheme := "http"
	if len(addr) > 4 && addr[len(addr)-4:] == ":443" {
		scheme = "https"
	}
	u, _ := url.Parse(fmt.Sprintf("%s://%s", scheme, addr))
	return &Endpoint{
		Address:   addr,
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
	// Use the endpoint's scheme (HTTP or HTTPS) as determined during initialization
	healthURL := fmt.Sprintf("%s://%s/health", ep.URL.Scheme, ep.Address)
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

// ProxyHandler handles incoming client requests.
func ProxyHandler(p *EndpointPool, cfg *config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// 1. Select the optimal backend
		targetEndpoint := p.Select()
		if targetEndpoint == nil {
			log.Println("[ProxyServer] No healthy endpoints available. Failing request.")
			http.Error(w, "Service Unavailable", http.StatusServiceUnavailable)
			return
		}

		// 2. Reverse Proxy and Forward Request
		proxy := httputil.NewSingleHostReverseProxy(targetEndpoint.URL)

		// Update director to ensure the host and scheme are set correctly for the backend
		proxy.Director = func(req *http.Request) {
			// Override the request URL to use the target endpoint's scheme and host
			req.URL.Scheme = targetEndpoint.URL.Scheme
			req.URL.Host = targetEndpoint.URL.Host
			req.URL.Path = r.URL.Path
			req.URL.RawPath = r.URL.RawPath
			req.URL.RawQuery = r.URL.RawQuery
			req.Host = targetEndpoint.URL.Host
			// Preserve headers but ensure Host is set correctly
			if req.Header == nil {
				req.Header = make(http.Header)
			}
		}

		// Custom handler for proxy response (where score update occurs)
		proxy.ModifyResponse = func(resp *http.Response) error {
			duration := time.Since(start)
			success := resp.StatusCode < 500 // Treat 5xx as failure

			// 4. Critical Step: Report back to ScoreTracker
			targetEndpoint.UpdateScore(success, duration, cfg.EWMAAlpha)
			return nil
		}

		proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
			log.Printf("[ProxyServer] Target %s failed: %v", targetEndpoint.Address, err)
			duration := time.Since(start)

			// 4. Critical Step: Report failure to ScoreTracker (Timeout Policy)
			targetEndpoint.UpdateScore(false, duration, cfg.EWMAAlpha) // Treat error/timeout as failure

			http.Error(w, "Gateway Timeout or Target Error", http.StatusGatewayTimeout)
		}

		// Read score with proper mutex protection to avoid data race
		targetEndpoint.Mutex.Lock()
		score := targetEndpoint.Score
		targetEndpoint.Mutex.Unlock()
		log.Printf("[ProxyServer] Routing request to %s://%s%s (Score: %.3f)", targetEndpoint.URL.Scheme, targetEndpoint.Address, r.URL.Path, score)
		proxy.ServeHTTP(w, r)
	}
}

// --- Main Application Setup ---
func main() {
	// Parse command-line flags
	configPath := flag.String("config", "./config.yaml", "Path to configuration file")
	flag.Parse()

	// Load configuration
	cfg, err := config.NewConfig(*configPath)
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	// Check if config file exists to provide appropriate log message
	if _, err := os.Stat(*configPath); os.IsNotExist(err) {
		log.Printf("Config file %s not found, using defaults and environment variables", *configPath)
	} else {
		log.Printf("Loaded configuration from %s", *configPath)
	}

	// Define the pool of backend endpoints
	pool := &EndpointPool{Endpoints: make([]*Endpoint, 0, len(cfg.BackendAddresses))}

	// Populate the EndpointPool
	for _, addr := range cfg.BackendAddresses {
		pool.Endpoints = append(pool.Endpoints, NewEndpoint(addr))
	}

	// 1. Start Management Layer routines
	go HealthChecker(pool, cfg)
	log.Println("HealthChecker started.")

	// 2. Start Request Layer (Proxy Server)
	proxyAddr := fmt.Sprintf(":%d", cfg.ProxyPort)
	log.Printf("Starting Load Balancing Proxy on %s", proxyAddr)

	http.HandleFunc("/", ProxyHandler(pool, cfg))

	if err := http.ListenAndServe(proxyAddr, nil); err != nil {
		log.Fatalf("Proxy failed to start: %v", err)
	}
}
