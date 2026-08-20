// Package host is the L1+L3 host driver. It dials the relay, holds the control
// connection, and bridges each accepted tunnel to a single l3.Adapter. On a
// tunnel open it accepts (tun.ok) and announces L2 negotiation; thereafter it
// hands opaque inbound frames to the adapter and writes adapter replies back
// over the same control connection, stamped with the tunnel id.
//
// This is the piktak-native (structured-adapter) host. The transparent host
// lives in internal/bridge. Both import wire and never the relay package.
package host

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/charlzyx/piktak/internal/l2"
	"github.com/charlzyx/piktak/internal/l3"
	"github.com/charlzyx/piktak/internal/wire"
)

// Host is configured by the binary: where the relay is, the L0 code, a stable
// host id clients discover it by, and the adapter to serve.
type Host struct {
	Addr    string
	Code    string
	HostID  string
	Adapter l3.Adapter
}

// Run dials the relay, registers, and serves tunnels until ctx is cancelled or
// the connection drops.
func (h *Host) Run(ctx context.Context) error {
	c, err := wire.DialTCP(ctx, h.Addr)
	if err != nil {
		return fmt.Errorf("host: dial relay: %w", err)
	}
	defer c.Close()

	if err := c.WriteFrame(ctx, wire.Envelope{
		T:       "hello",
		Payload: wire.MustJSON(helloBody{Role: "host", Protocol: wire.ProtocolTCP, Version: wire.Version, Code: h.Code, HostID: h.HostID}),
	}); err != nil {
		return fmt.Errorf("host: hello: %w", err)
	}
	ok, err := c.ReadFrame(ctx)
	if err != nil {
		return fmt.Errorf("host: hello read: %w", err)
	}
	if ok.T != "hello.ok" {
		return fmt.Errorf("host: hello rejected: %s", ok.T)
	}
	var hok helloOK
	decode(ok.Payload, &hok)
	log.Printf("host online: id=%s adapter=%s v%d", hok.HostID, h.Adapter.Name(), h.Adapter.Version())

	hub := &hub{adapter: h.Adapter, conn: c}
	for {
		env, err := c.ReadFrame(ctx)
		if err != nil {
			return fmt.Errorf("host: read: %w", err)
		}
		if err := hub.handle(ctx, env); err != nil {
			return err
		}
	}
}

type hub struct {
	adapter l3.Adapter
	conn    wire.FrameConn
}

func (h *hub) handle(ctx context.Context, env wire.Envelope) error {
	switch env.T {
	case "tun.open":
		// Accept and announce L2 negotiation.
		if err := h.conn.WriteFrame(ctx, wire.Envelope{T: "tun.ok", Tunnel: env.Tunnel, ID: env.ID}); err != nil {
			return err
		}
		return h.conn.WriteFrame(ctx, wire.Envelope{
			T:       "neg",
			Tunnel:  env.Tunnel,
			Payload: wire.MustJSON(l2.Negotiate{Adapter: h.adapter.Name(), Version: h.adapter.Version(), Caps: h.adapter.Caps()}),
		})
	case "neg.ack":
		// Client accepted the adapter; nothing to do for v0.
		return nil
	case "tun.close":
		// Tunnel torn down; v0 adapters are stateless per tunnel.
		return nil
	default:
		if env.Tunnel == "" {
			return nil
		}
		// Per-frame send that stamps the tunnel id on every adapter reply.
		// The adapter itself never sets Tunnel; routing is the host driver's
		// job, and the relay routes opaque frames by exactly this field.
		send := func(out wire.Envelope) error {
			out.Tunnel = env.Tunnel
			return h.conn.WriteFrame(context.Background(), out)
		}
		if err := h.adapter.Handle(ctx, env, send); err != nil {
			return h.conn.WriteFrame(ctx, wire.Envelope{
				T:       "neg.err",
				Tunnel:  env.Tunnel,
				ID:      env.ID,
				Payload: wire.MustJSON(l2.Error{Code: "ADAPTER", Message: err.Error()}),
			})
		}
		return nil
	}
}

// helloBody mirrors the relay's private control frame. The wire contract is
// the JSON shape; the struct is re-declared here so host does not import relay.
type helloBody struct {
	Role     string `json:"role,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Version  int    `json:"version,omitempty"`
	Code     string `json:"code,omitempty"`
	HostID   string `json:"hostId,omitempty"`
}

type helloOK struct {
	HostID string `json:"hostId,omitempty"`
}

func decode(p json.RawMessage, v any) {
	if len(p) > 0 {
		_ = json.Unmarshal(p, v)
	}
}
