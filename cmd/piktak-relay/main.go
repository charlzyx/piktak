// Command piktak-relay runs the L1 broker: a wire listener for control and
// data-attach, and a raw ingress listener for inbound application traffic.
// Pairing is machine-code (PIKTAK_CODES, comma-separated).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/charlzyx/piktak/internal/l0"
	"github.com/charlzyx/piktak/internal/relay"
)

func main() {
	statePath := env("PIKTAK_STATE", "")
	if len(os.Args) > 1 && os.Args[1] == "pair" {
		if statePath == "" {
			log.Fatal("piktak-relay: PIKTAK_STATE is required for pair")
		}
		code, err := l0.GeneratePairingCode()
		if err != nil {
			log.Fatalf("piktak-relay: generate pairing code: %v", err)
		}
		if err := l0.WritePairingCode(statePath, code); err != nil {
			log.Fatalf("piktak-relay: write pairing code: %v", err)
		}
		fmt.Println(code)
		return
	}
	configuredCodes := env("PIKTAK_CODES", "")
	pairCode := env("PIKTAK_PAIR_CODE", "")
	if pairCode == "" && configuredCodes == "" && statePath == "" {
		log.Fatal("piktak-relay: PIKTAK_PAIR_CODE, PIKTAK_CODES, or PIKTAK_STATE is required")
	}
	var pairer l0.Pairer
	if pairCode != "" || statePath != "" {
		p, err := l0.NewDynamicPairer(pairCode, statePath)
		if err != nil {
			log.Fatalf("piktak-relay: dynamic pairing: %v", err)
		}
		pairer = p
	} else {
		pairer = l0.NewMachineCode(strings.Split(configuredCodes, ",")...)
	}
	r := relay.New(env("PIKTAK_ADDR", ":7681"), env("PIKTAK_INGRESS", ":7682"), pairer)
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
