// Command piktak-relay runs the L1 broker: a wire listener for control and
// data-attach, and a raw ingress listener for inbound application traffic.
// Pairing is machine-code (PIKTAK_CODES, comma-separated).
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/charlzyx/piktak/internal/l0"
	"github.com/charlzyx/piktak/internal/relay"
)

func main() {
	configuredCodes := env("PIKTAK_CODES", "")
	if configuredCodes == "" {
		log.Fatal("piktak-relay: PIKTAK_CODES is required")
	}
	codes := strings.Split(configuredCodes, ",")
	r := relay.New(
		env("PIKTAK_ADDR", ":7681"),
		env("PIKTAK_INGRESS", ":7682"),
		l0.NewMachineCode(codes...),
	)
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := r.Serve(ctx); err != nil {
		log.Fatalf("relay: %v", err)
	}
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
