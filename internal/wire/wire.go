// Package wire is the transport-agnostic framing layer shared by every PIK.TAK
// component. It defines the one envelope the broker understands well enough
// to route, and the FrameConn interface L1 runs on.
//
// v0 ships a TCP + newline-delimited-JSON transport (stdlib only). A WebSocket
// transport would be another implementation of FrameConn; nothing above this
// package would change.
package wire

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync"
)

// Protocol identifiers + version for hello negotiation. Both ends (the relay
// and thost/host/client) must agree; the relay rejects a hello whose protocol
// or version it does not speak, so a stale or mismatched peer fails loudly
// instead of silently drifting.
const (
	ProtocolTCP = "piktak-tcp" // JSONL-over-TCP relay protocol (piktak-go)
	ProtocolWS  = "piktak-ws"  // WebSocket-framed relay protocol (piktak-cf / self-hosted ws relay)
	Version     = 1
)

// Envelope is the only structure the broker (L1) decodes enough to route on.
// ID and Payload are opaque to the broker: ID is a correlation id echoed by
// adapters, Payload is whatever L2/L3 put there. The broker never unmarshals
// Payload except for the handful of control frames it owns (see decode.go in
// the relay).
type Envelope struct {
	T       string          `json:"t"`             // frame type
	Tunnel  string          `json:"tun,omitempty"` // tunnel id; broker routes opaque frames on this
	ID      json.RawMessage `json:"id,omitempty"`  // correlation id, opaque, echoed by adapters
	Payload json.RawMessage `json:"p,omitempty"`   // opaque per-layer payload
}

// FrameConn is the transport L1 runs on. One side reads framed Envelopes,
// writes framed Envelopes. Reads block until a full frame arrives or the
// connection closes.
type FrameConn interface {
	ReadFrame(ctx context.Context) (Envelope, error)
	WriteFrame(ctx context.Context, env Envelope) error
	Close() error
}

// TCPConn is a newline-delimited JSON FrameConn over a single TCP connection.
type TCPConn struct {
	c net.Conn
	r *bufio.Reader
	m sync.Mutex
}

// DialTCP dials a TCP address and returns a FrameConn.
func DialTCP(ctx context.Context, addr string) (*TCPConn, error) {
	d := net.Dialer{}
	c, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	return &TCPConn{c: c, r: bufio.NewReader(c)}, nil
}

// ServeTCP wraps an already-accepted TCP connection as a FrameConn.
func ServeTCP(conn net.Conn) *TCPConn {
	return &TCPConn{c: conn, r: bufio.NewReader(conn)}
}

func (t *TCPConn) ReadFrame(ctx context.Context) (Envelope, error) {
	// v0: ctx is not applied to a blocking TCP read. The production path is
	// SetDeadline(ctx); here reads are line-bounded and connection close
	// unblocks them with io.EOF.
	_ = ctx
	line, err := t.r.ReadBytes('\n')
	if err != nil {
		return Envelope{}, err
	}
	var env Envelope
	if err := json.Unmarshal(line, &env); err != nil {
		return Envelope{}, fmt.Errorf("wire: bad frame: %w", err)
	}
	return env, nil
}

func (t *TCPConn) WriteFrame(ctx context.Context, env Envelope) error {
	_ = ctx
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("wire: marshal: %w", err)
	}
	t.m.Lock()
	defer t.m.Unlock()
	if _, err := t.c.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}

func (t *TCPConn) Close() error { return t.c.Close() }

// Raw returns the underlying connection and buffered reader for raw-byte
// continuation after framed reads. A caller that has finished framing (for
// example a data channel that read its one attach line) uses these to splice
// raw bytes without further framing. The reader drains any bytes already
// buffered ahead of the last frame boundary before reading the socket.
func (t *TCPConn) Raw() (net.Conn, *bufio.Reader) {
	return t.c, t.r
}

// MustJSON marshals v to a RawMessage, panicking only on impossible inputs.
// Used to build Envelope.Payload for the control frames each layer owns.
func MustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		// Only reachable for types that cannot be JSON-encoded (e.g. chan,
		// func); none of those appear in payloads here.
		panic(fmt.Sprintf("wire: mustJSON: %v", err))
	}
	return b
}

// ID builds a JSON-string raw correlation id from a plain string.
func ID(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}
