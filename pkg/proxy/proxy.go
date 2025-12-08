package proxy

import (
	"log"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/archway-network/lambo/pkg/config"
	"github.com/archway-network/lambo/pkg/manager"
)

// ProxyHandler handles incoming client requests.
func ProxyHandler(p *manager.EndpointPool, cfg *config.Config) http.HandlerFunc {
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

