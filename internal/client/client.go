// Package client is the L1+L2 client driver. It dials the relay, authenticates,
// opens a tunnel to a host, completes L2 negotiation (verifying the adapter
// name and version it expected), and returns a Tunnel the caller uses to
// exchange opaque L3 frames.
//
// Like the host driver, it imports wire and l2 but never the relay package.
package client

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/charlzyx/piktak/internal/l2"
	"github.com/charlzyx/piktak/internal/wire"
)

// Client is configured by the binary: where the relay is, the L0 code, the
// host id to join, and the adapter/version it expects after L2 negotiation.
type Client struct {
	Addr    string
	Code    string
	HostID  string
	Adapter string // expected adapter name; "" = accept any
	Version int    // expected version; 0 = accept any
}

// Tunnel is an established, negotiated tunnel. Send stamps the tunnel id;
// Recv blocks for the next frame from the host end.
type Tunnel struct {
	ID   string
	conn wire.FrameConn
}

// Connect dials, authenticates, opens a tunnel, and completes L2 negotiation.
func (c *Client) Connect(ctx context.Context) (*Tunnel, error) {
	conn, err := wire.DialTCP(ctx, c.Addr)
	if err != nil {
		return nil, fmt.Errorf("client: dial relay: %w", err)
	}
	if err := conn.WriteFrame(ctx, wire.Envelope{
		T:       "hello",
		Payload: wire.MustJSON(helloBody{Role: "client", Protocol: wire.ProtocolTCP, Version: wire.Version, Code: c.Code, HostID: c.HostID}),
	}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("client: hello: %w", err)
	}
	ok, err := conn.ReadFrame(ctx)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("client: hello read: %w", err)
	}
	if ok.T != "hello.ok" {
		conn.Close()
		return nil, fmt.Errorf("client: hello rejected: %s", ok.T)
	}

	// Open a tunnel.
	if err := conn.WriteFrame(ctx, wire.Envelope{T: "tun.open", ID: wire.ID("1")}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("client: tun.open: %w", err)
	}
	ack, err := conn.ReadFrame(ctx)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("client: tun.open read: %w", err)
	}
	if ack.T != "tun.ok" {
		conn.Close()
		return nil, fmt.Errorf("client: tun.open rejected: %s", ack.T)
	}
	tun := ack.Tunnel

	// L2 negotiation: host announces, we verify and ack.
	neg, err := conn.ReadFrame(ctx)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("client: negotiate read: %w", err)
	}
	if neg.T != "neg" {
		conn.Close()
		return nil, fmt.Errorf("client: expected neg, got %s", neg.T)
	}
	var n l2.Negotiate
	decode(neg.Payload, &n)
	if c.Adapter != "" && n.Adapter != c.Adapter {
		conn.Close()
		return nil, fmt.Errorf("client: adapter mismatch: want %s got %s", c.Adapter, n.Adapter)
	}
	if c.Version != 0 && n.Version != c.Version {
		conn.Close()
		return nil, fmt.Errorf("client: version mismatch: want %d got %d", c.Version, n.Version)
	}
	if err := conn.WriteFrame(ctx, wire.Envelope{
		T:       "neg.ack",
		Tunnel:  tun,
		Payload: wire.MustJSON(l2.Ack{OK: true, Version: n.Version}),
	}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("client: neg.ack: %w", err)
	}
	return &Tunnel{ID: tun, conn: conn}, nil
}

// Send writes an opaque L3 frame, stamping the tunnel id.
func (t *Tunnel) Send(ctx context.Context, env wire.Envelope) error {
	env.Tunnel = t.ID
	return t.conn.WriteFrame(ctx, env)
}

// Recv blocks for the next frame from the host end of the tunnel.
func (t *Tunnel) Recv(ctx context.Context) (wire.Envelope, error) {
	return t.conn.ReadFrame(ctx)
}

// Close tears down the tunnel and the underlying connection.
func (t *Tunnel) Close() error {
	_ = t.conn.WriteFrame(context.Background(), wire.Envelope{T: "tun.close", Tunnel: t.ID})
	return t.conn.Close()
}

type helloBody struct {
	Role     string `json:"role,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Version  int    `json:"version,omitempty"`
	Code     string `json:"code,omitempty"`
	HostID   string `json:"hostId,omitempty"`
}

func decode(p json.RawMessage, v any) {
	if len(p) > 0 {
		_ = json.Unmarshal(p, v)
	}
}
