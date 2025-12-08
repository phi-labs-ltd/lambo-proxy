package config

import (
	"fmt"
	"os"
	"time"

	"github.com/caarlos0/env/v9"
	"gopkg.in/yaml.v3"
)

// Config holds all configuration for the load balancer.
type Config struct {
	ProxyPort           int           `yaml:"proxy_port" env:"PROXY_PORT" default:"8080"`
	HealthCheckInterval time.Duration `yaml:"health_check_interval" env:"HEALTH_CHECK_INTERVAL" default:"5s"`
	HealthCheckFailures int           `yaml:"health_check_failures" env:"HEALTH_CHECK_FAILURES" default:"3"`
	EWMAAlpha           float64       `yaml:"ewma_alpha" env:"EWMA_ALPHA" default:"0.1"`
	BackendAddresses    []string      `yaml:"backend_addresses" env:"BACKEND_ADDRESSES" envSeparator:","`
}

// NewConfig loads configuration from the specified YAML file and environment variables.
// Environment variables override YAML values.
func NewConfig(configPath string) (*Config, error) {
	config := &Config{}

	// Set defaults first
	config.setDefaults()

	// Load from YAML file if it exists
	file, err := os.Open(configPath)
	if err != nil {
		// If file doesn't exist, continue with defaults and environment variables
		if os.IsNotExist(err) {
			// Log that we're using defaults/env vars, but don't fail
			// This allows the application to run without a config file
		} else {
			// For other errors (permission denied, etc.), return the error
			return nil, fmt.Errorf("failed to open config file %s: %w", configPath, err)
		}
	} else {
		// File exists, decode it
		defer file.Close()
		d := yaml.NewDecoder(file)
		if err := d.Decode(config); err != nil {
			return nil, fmt.Errorf("failed to decode config file: %w", err)
		}
	}

	// Override with environment variables
	if err := env.Parse(config); err != nil {
		return nil, fmt.Errorf("failed to parse environment variables: %w", err)
	}

	// Validate configuration
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	return config, nil
}

// setDefaults sets default values for configuration fields.
func (c *Config) setDefaults() {
	if c.ProxyPort == 0 {
		c.ProxyPort = 8080
	}
	if c.HealthCheckInterval == 0 {
		c.HealthCheckInterval = 5 * time.Second
	}
	if c.HealthCheckFailures == 0 {
		c.HealthCheckFailures = 3
	}
	if c.EWMAAlpha == 0 {
		c.EWMAAlpha = 0.1
	}
	if len(c.BackendAddresses) == 0 {
		c.BackendAddresses = []string{
			"rpc-osmosis.ecostake.com:443",
			"osmosis-rpc.polkachu.com:443",
			"rpc.osmosis.validatus.com:443",
		}
	}
}

// Validate performs validation on the configuration.
func (c *Config) Validate() error {
	if c.ProxyPort < 1 || c.ProxyPort > 65535 {
		return fmt.Errorf("proxy_port must be between 1 and 65535, got %d", c.ProxyPort)
	}
	if c.HealthCheckInterval <= 0 {
		return fmt.Errorf("health_check_interval must be positive, got %v", c.HealthCheckInterval)
	}
	if c.HealthCheckFailures < 1 {
		return fmt.Errorf("health_check_failures must be at least 1, got %d", c.HealthCheckFailures)
	}
	if c.EWMAAlpha <= 0 || c.EWMAAlpha > 1 {
		return fmt.Errorf("ewma_alpha must be between 0 and 1, got %f", c.EWMAAlpha)
	}
	if len(c.BackendAddresses) == 0 {
		return fmt.Errorf("backend_addresses must contain at least one address")
	}
	return nil
}
