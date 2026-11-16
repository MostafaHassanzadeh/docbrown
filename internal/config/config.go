// Package config loads docbrown.yaml.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Node is one PostgreSQL server, reached directly (not through the endpoint).
type Node struct {
	Name string `yaml:"name"`
	DSN  string `yaml:"dsn"`
}

// Probe controls how often docbrown writes, reads and polls.
type Probe struct {
	IntervalMS     int `yaml:"interval_ms"`
	TimeoutMS      int `yaml:"timeout_ms"`
	PollIntervalMS int `yaml:"poll_interval_ms"`
}

// Config is the whole docbrown.yaml file.
type Config struct {
	// Endpoint is the DSN your applications use (VIP, HAProxy, DNS name...).
	Endpoint string `yaml:"endpoint"`
	// Nodes are the individual servers, polled directly.
	Nodes []Node `yaml:"nodes"`
	Probe Probe  `yaml:"probe"`
	// HealthyLagBytes is the replay lag under which a replica counts as caught up.
	HealthyLagBytes int64  `yaml:"healthy_lag_bytes"`
	RunsDir         string `yaml:"runs_dir"`
	ReportsDir      string `yaml:"reports_dir"`
}

// Load reads and validates a config file, filling defaults.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	c.defaults()
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return &c, nil
}

func (c *Config) defaults() {
	if c.Probe.IntervalMS <= 0 {
		c.Probe.IntervalMS = 50
	}
	if c.Probe.TimeoutMS <= 0 {
		c.Probe.TimeoutMS = 1000
	}
	if c.Probe.PollIntervalMS <= 0 {
		c.Probe.PollIntervalMS = 250
	}
	if c.HealthyLagBytes <= 0 {
		c.HealthyLagBytes = 1 << 20 // 1 MiB
	}
	if c.RunsDir == "" {
		c.RunsDir = "runs"
	}
	if c.ReportsDir == "" {
		c.ReportsDir = "reports"
	}
}

func (c *Config) validate() error {
	if c.Endpoint == "" {
		return errors.New("endpoint is required")
	}
	if len(c.Nodes) == 0 {
		return errors.New("at least one node is required")
	}
	seen := map[string]bool{}
	for i, n := range c.Nodes {
		if n.Name == "" || n.DSN == "" {
			return fmt.Errorf("nodes[%d]: name and dsn are required", i)
		}
		if seen[n.Name] {
			return fmt.Errorf("duplicate node name %q", n.Name)
		}
		seen[n.Name] = true
	}
	return nil
}

// Interval returns the probe interval.
func (c *Config) Interval() time.Duration {
	return time.Duration(c.Probe.IntervalMS) * time.Millisecond
}

// Timeout returns the per-operation timeout.
func (c *Config) Timeout() time.Duration { return time.Duration(c.Probe.TimeoutMS) * time.Millisecond }

// PollInterval returns the node polling interval.
func (c *Config) PollInterval() time.Duration {
	return time.Duration(c.Probe.PollIntervalMS) * time.Millisecond
}
