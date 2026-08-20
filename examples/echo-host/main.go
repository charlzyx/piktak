// Command piktak-host runs the piktak-native host driver with the echo adapter.
// Set PIKTAK_HOST_ID to the stable id clients discover it by, PIKTAK_CODE to the
// machine code the relay's allowlist holds.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/charlzyx/piktak/internal/host"
	"github.com/charlzyx/piktak/internal/l3/echo"
)

func main() {
	h := &host.Host{
		Addr:    env("PIKTAK_RELAY", "127.0.0.1:7681"),
		Code:    env("PIKTAK_CODE", "12345678"),
		HostID:  env("PIKTAK_HOST_ID", "host-1"),
		Adapter: echo.Adapter{},
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := h.Run(ctx); err != nil {
		log.Fatalf("host: %v", err)
	}
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
