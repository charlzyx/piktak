// Command piktakd runs the transparent host — a single standalone binary
// with one or many forwards, configured either by flags/env (single service)
// or by a YAML config file (-config) with a services list. Each service picks
// its wire protocol explicitly: "ws" = the WebSocket-framed relay protocol
// (a CF Worker or any self-hosted server implementing it), "tcp" = the
// JSONL-over-TCP relay protocol (the self-hosted box relay). The relay URL's
// scheme (ws/wss) is encryption only, never the protocol selector.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/charlzyx/piktak/internal/bridge"
	"github.com/charlzyx/piktak/internal/status"
	"github.com/charlzyx/piktak/internal/wsbridge"
)

func main() {
	configPath := flag.String("config", env("PIKTAK_CONFIG", ""), "YAML config file (multi-service; see README). Default: flags/env, single service")
	relay := flag.String("relay", env("PIKTAK_RELAY", "127.0.0.1:7681"), "relay address (single-service mode)")
	code := flag.String("code", env("PIKTAK_CODE", ""), "machine code (L0 pairing secret; required)")
	local := flag.String("local", env("PIKTAK_LOCAL", "127.0.0.1:7531"), "local address to expose")
	name := flag.String("name", env("PIKTAK_NAME", ""), "routing key (single-service mode; the relay routes this host's WS to its DO, e.g. the browser subdomain)")
	rewriteHost := flag.Bool("rewrite-host", env("PIKTAK_REWRITE_HOST", "0") == "1", "loopback compat: rewrite Host to local + strip browser origin markers (for backends that trust loopback/same-origin, e.g. dsh)")
	if len(os.Args) > 1 && os.Args[1] == "help" {
		printServiceHelp(os.Args[2:])
		return
	}
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Resolve the concrete service list.
	var services []ServiceConfig
	var cfg *Config
	if *configPath != "" {
		var err error
		cfg, err = loadConfig(*configPath)
		if err != nil {
			log.Fatalf("piktakd: %v", err)
		}
		services = cfg.expand()
	} else {
		services = []ServiceConfig{{
			Name:        *name,
			Protocol:    guessProtocol(*relay),
			Relay:       *relay,
			Code:        *code,
			Local:       *local,
			RewriteHost: *rewriteHost,
		}}
	}

	for _, s := range services {
		if s.Code == "" {
			log.Fatalf("piktakd[%s]: machine code is required (set code in config, -code, or PIKTAK_CODE)", serviceLabel(s))
		}
	}

	if cfg != nil && cfg.Status {
		statusRelay := cfg.Relay
		if statusRelay == "" && len(services) > 0 {
			statusRelay = services[0].Relay
		}
		statusCode := cfg.Code
		if statusCode == "" && len(services) > 0 {
			statusCode = services[0].Code
		}
		state := &status.State{
			Name: "piktakd", Protocol: "local", Relay: statusRelay,
			Code: statusCode, Local: "built-in local status", Started: time.Now(),
		}
		addr := cfg.StatusAddr
		if addr == "" {
			addr = "127.0.0.1:0"
		}
		bound, err := status.ServeAddr(ctx, state, addr)
		if err != nil {
			log.Fatalf("piktakd: status: %v", err)
		}
		log.Printf("piktakd local status page: http://%s/ (API: http://%s/api/status)", bound, bound)
	}

	var wg sync.WaitGroup
	for i := range services {
		if services[i].Local == "status" {
			state := &status.State{
				Name: services[i].Name, Protocol: services[i].Protocol,
				Relay: services[i].Relay, Code: services[i].Code,
				Local: "built-in status page", Started: time.Now(),
			}
			local, err := status.Serve(ctx, state)
			if err != nil {
				log.Fatalf("piktakd: %v", err)
			}
			services[i].Local = local
			services[i].Status = state
			log.Printf("piktakd[%s] status page: http://%s/ (API: http://%s/api/status)", serviceLabel(services[i]), local, local)
		}
	}
	for _, s := range services {
		wg.Add(1)
		go func(s ServiceConfig) {
			defer wg.Done()
			if err := runService(ctx, s); err != nil && ctx.Err() == nil {
				log.Printf("piktakd[%s]: %v", serviceLabel(s), err)
			}
		}(s)
	}
	wg.Wait()
}

func printServiceHelp(args []string) {
	fs := flag.NewFlagSet("piktakd help", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	configPath := fs.String("config", env("PIKTAK_CONFIG", ""), "YAML config file")
	relay := fs.String("relay", env("PIKTAK_RELAY", "127.0.0.1:7681"), "relay address")
	code := fs.String("code", env("PIKTAK_CODE", ""), "machine code")
	local := fs.String("local", env("PIKTAK_LOCAL", "127.0.0.1:7531"), "local address")
	name := fs.String("name", env("PIKTAK_NAME", ""), "service name")
	if err := fs.Parse(args); err != nil {
		return
	}

	services := []ServiceConfig{}
	if *configPath != "" {
		cfg, err := loadConfig(*configPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "piktakd: %v\\n", err)
			return
		}
		services = cfg.expand()
	} else {
		services = append(services, ServiceConfig{Name: *name, Protocol: guessProtocol(*relay), Relay: *relay, Code: *code, Local: *local})
	}

	fmt.Println("piktakd")
	fmt.Println("services:")
	for i, s := range services {
		label := s.Name
		if label == "" {
			label = fmt.Sprintf("service-%d", i+1)
		}
		fmt.Printf("  %s\n", label)
		fmt.Printf("    protocol: %s\n", s.Protocol)
		fmt.Printf("    relay:    %s\n", s.Relay)
		fmt.Printf("    local:    %s\n", s.Local)
		if s.Local == "status" {
			fmt.Println("    html:     built-in status page (starts with this service)")
			fmt.Println("    api:      /api/status")
		}
	}
	if *configPath != "" {
		cfg, err := loadConfig(*configPath)
		if err == nil && cfg.Status {
			addr := cfg.StatusAddr
			if addr == "" {
				addr = "127.0.0.1:0"
			}
			fmt.Printf("status:   enabled (local only, %s)\n", addr)
			fmt.Printf("html:     http://%s/\n", addr)
			fmt.Printf("api:      http://%s/api/status\n", addr)
		}
	}
	fmt.Println()
	fmt.Println("The status page is local-only and does not create a relay route.")
	fmt.Println("When status_addr uses port 0, the startup log reports the assigned port.")
}

func serviceLabel(s ServiceConfig) string {
	if s.Name != "" {
		return s.Name
	}
	return s.Relay
}

func runService(ctx context.Context, s ServiceConfig) error {
	if s.Protocol == "ws" {
		h := &wsbridge.Host{Relay: s.Relay, Code: s.Code, Local: s.Local, RewriteHost: s.RewriteHost, Name: s.Name, Status: s.Status}
		return h.Run(ctx)
	}
	h := &bridge.Host{Addr: s.Relay, Code: s.Code, Local: s.Local, HostID: s.Name, Status: s.Status}
	return h.Run(ctx)
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
