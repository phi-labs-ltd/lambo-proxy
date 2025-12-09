package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/archway-network/lambo/pkg/config"
	"github.com/archway-network/lambo/pkg/manager"
	"github.com/archway-network/lambo/pkg/proxy"
)

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
	pool := &manager.EndpointPool{Endpoints: make([]*manager.Endpoint, 0, len(cfg.BackendAddresses))}

	// Populate the EndpointPool
	for _, addr := range cfg.BackendAddresses {
		pool.Endpoints = append(pool.Endpoints, manager.NewEndpoint(addr))
	}

	// 1. Start Management Layer routines
	go manager.HealthChecker(pool, cfg)
	log.Println("HealthChecker started.")

	// 2. Start Request Layer (Proxy Server)
	proxyAddr := fmt.Sprintf(":%d", cfg.ProxyPort)
	log.Printf("Starting Load Balancing Proxy on %s", proxyAddr)

	http.HandleFunc("/", proxy.ProxyHandler(pool, cfg))

	if err := http.ListenAndServe(proxyAddr, nil); err != nil {
		log.Fatalf("Proxy failed to start: %v", err)
	}
}
