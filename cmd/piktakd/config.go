package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/charlzyx/piktak/internal/status"

	"gopkg.in/yaml.v3"
)

// ServiceConfig is one forward in a multi-service config file. Protocol names
// the wire protocol, NOT the product: "ws" = the WebSocket-framed relay
// protocol (a CF Worker or any self-hosted server implementing it), "tcp" =
// the JSONL-over-TCP relay protocol (the self-hosted box relay). Empty
// protocol falls back to guessing from the relay scheme (ws/wss -> ws).
type ServiceConfig struct {
	Name        string        `yaml:"name"`         // routing key (piktak-cf: the browser subdomain)
	Protocol    string        `yaml:"protocol"`     // "ws" | "tcp"; empty = guess from scheme
	Relay       string        `yaml:"relay"`        // relay URL/address; scheme (ws/wss) is encryption only
	Code        string        `yaml:"code"`         // machine code (L0 pairing secret)
	Local       string        `yaml:"local"`        // local addr to expose
	RewriteHost bool          `yaml:"rewrite_host"` // loopback compat (backends that trust loopback/same-origin)
	Status      *status.State `yaml:"-"`
}

// Config is the file schema: optional top-level defaults + a services list.
// With no services, the top-level fields act as a single-service config.
type Config struct {
	Relay       string          `yaml:"relay"`
	Code        string          `yaml:"code"`
	Local       string          `yaml:"local"`
	RewriteHost bool            `yaml:"rewrite_host"`
	Status      bool            `yaml:"status"`
	StatusAddr  string          `yaml:"status_addr"`
	Services    []ServiceConfig `yaml:"services"`
}

func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &c, nil
}

// expand returns the concrete service list, filling defaults from the top-level
// fields and protocol from the relay scheme when omitted.
func (c *Config) expand() []ServiceConfig {
	if len(c.Services) == 0 {
		return []ServiceConfig{{
			Protocol:    guessProtocol(c.Relay),
			Relay:       c.Relay,
			Code:        c.Code,
			Local:       c.Local,
			RewriteHost: c.RewriteHost,
		}}
	}
	out := make([]ServiceConfig, 0, len(c.Services))
	for _, s := range c.Services {
		if s.Relay == "" {
			s.Relay = c.Relay
		}
		if s.Code == "" {
			s.Code = c.Code
		}
		if s.Local == "" {
			s.Local = c.Local
		}
		if s.Protocol == "" {
			s.Protocol = guessProtocol(s.Relay)
		}
		out = append(out, s)
	}
	return out
}

func guessProtocol(relay string) string {
	if strings.HasPrefix(relay, "ws://") || strings.HasPrefix(relay, "wss://") {
		return "ws"
	}
	return "tcp"
}
