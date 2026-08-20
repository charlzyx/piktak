// Command piktak-client joins a host through the relay, opens a tunnel, completes
// L2 negotiation, and sends a couple of echo frames to prove the full loop.
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/charlzyx/piktak/internal/client"
	"github.com/charlzyx/piktak/internal/wire"
)

func main() {
	c := &client.Client{
		Addr:    env("PIKTAK_RELAY", "127.0.0.1:7681"),
		Code:    env("PIKTAK_CODE", "12345678"),
		HostID:  env("PIKTAK_HOST_ID", "host-1"),
		Adapter: "echo",
		Version: 1,
	}
	ctx := context.Background()
	tun, err := c.Connect(ctx)
	if err != nil {
		log.Fatalf("client: %v", err)
	}
	defer tun.Close()

	for _, text := range []string{"hello PIK.TAK", "second frame"} {
		if err := tun.Send(ctx, wire.Envelope{
			T:       "echo",
			ID:      wire.ID(text),
			Payload: wire.MustJSON(map[string]string{"text": text}),
		}); err != nil {
			log.Fatalf("send: %v", err)
		}
		reply, err := tun.Recv(ctx)
		if err != nil {
			log.Fatalf("recv: %v", err)
		}
		fmt.Printf("reply: t=%s id=%s payload=%s\n", reply.T, string(reply.ID), string(reply.Payload))
	}
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
