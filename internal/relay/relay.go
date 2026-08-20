// Package relay is the L1 broker.
//
// It runs two listeners:
//   - a wire listener (newline-JSON) for control connections and for the one
//     framed handshake that opens a raw data channel;
//   - an ingress listener (raw TCP) for inbound application connections, e.g.
//     a browser.
//
// Two forwarding modes coexist on one relay:
//
//   - PIK.TAK-native (structured adapters): a host and a client both speak PIK.TAK's
//     envelope; the relay pairs a tunnel by id and forwards opaque framed
//     messages. Used by L3 adapters like echo.
//
//   - Transparent (raw byte pipe): a host exposes a local TCP address behind
//     the ingress port. An inbound browser connection is spliced byte-for-byte
//     to a raw data channel the host opens on demand. The relay never parses
//     HTTP or any application bytes — it is a dumb pipe, fully transparent to
//     the two ends (like lay's relay is to Pi RPC).
//
// DESIGN INVARIANT: this package imports ONLY wire and l0. It never imports
// l2 or l3 — the broker cannot learn any application vocabulary. The import
// graph enforces the four-layer split.
package relay

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charlzyx/piktak/internal/l0"
	"github.com/charlzyx/piktak/internal/wire"
)

// Relay is the L1 broker.
type Relay struct {
	Addr    string // wire listener: hello + data.attach
	Ingress string // ingress listener: raw, application-facing
	L0      l0.Pairer

	mu       sync.Mutex
	hosts    map[string]*hostSession
	tunnels  map[string]*tunnelSession // piktak-native tunnels by tun id
	rawConns map[string]*rawConn       // transparent inbound by connId
	nextID   uint64
	nextTun  uint64
	nextConn uint64
}

// New returns a Relay listening at addr (wire) and ingress (raw), authorizing
// with the L0 pairer.
func New(addr, ingress string, pairer l0.Pairer) *Relay {
	return &Relay{
		Addr: addr, Ingress: ingress, L0: pairer,
		hosts:    map[string]*hostSession{},
		tunnels:  map[string]*tunnelSession{},
		rawConns: map[string]*rawConn{},
	}
}

// Serve runs both listeners until ctx is cancelled.
func (r *Relay) Serve(ctx context.Context) error {
	lnWire, err := net.Listen("tcp", r.Addr)
	if err != nil {
		return fmt.Errorf("relay: wire listen: %w", err)
	}
	lnIn, err := net.Listen("tcp", r.Ingress)
	if err != nil {
		lnWire.Close()
		return fmt.Errorf("relay: ingress listen: %w", err)
	}
	log.Printf("relay wire=%s ingress=%s", r.Addr, r.Ingress)

	go r.acceptWire(ctx, lnWire)
	go r.acceptIngress(ctx, lnIn)

	<-ctx.Done()
	lnWire.Close()
	lnIn.Close()
	return nil
}

func (r *Relay) acceptWire(ctx context.Context, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("wire accept: %v", err)
			return
		}
		go r.serveWire(ctx, c)
	}
}

// serveWire reads the first framed line and dispatches: hello -> control
// session, data.attach -> raw data channel. Anything else is dropped.
func (r *Relay) serveWire(ctx context.Context, raw net.Conn) {
	c := wire.ServeTCP(raw)
	defer c.Close()
	first, err := c.ReadFrame(ctx)
	if err != nil {
		return
	}
	switch first.T {
	case "hello":
		r.handleHello(ctx, c, first)
	case "data.attach":
		r.serveData(ctx, c, first)
	default:
		// unknown first frame; drop.
	}
}

func (r *Relay) handleHello(ctx context.Context, c wire.FrameConn, hello wire.Envelope) {
	var hb helloBody
	decode(hello.Payload, &hb)
	// Protocol alignment: this relay speaks piktak-tcp. A peer that declares a
	// different protocol or version is rejected loudly (a stale thost hitting
	// a new relay would otherwise drift silently). Empty protocol/version
	// (legacy peers) is tolerated.
	if hb.Protocol != "" && (hb.Protocol != wire.ProtocolTCP || hb.Version != wire.Version) {
		_ = c.WriteFrame(ctx, wire.Envelope{
			T: "hello.err",
			Payload: wire.MustJSON(errBody{
				Code:    "PROTOCOL_MISMATCH",
				Message: "relay speaks " + wire.ProtocolTCP + "/" + itoa(wire.Version),
			}),
		})
		return
	}
	if hb.Code != "" || (hb.PairingCode == "" && hb.Credential == "") {
		_ = c.WriteFrame(ctx, wire.Envelope{T: "hello.err", Payload: wire.MustJSON(errBody{Code: "PAIRING_REQUIRED"})})
		return
	}
	var issued string
	var identity l0.Identity
	var authErr error
	if hb.PairingCode != "" {
		issuer, ok := r.L0.(l0.CredentialIssuer)
		if !ok {
			authErr = l0.ErrUnauthorized
		} else {
			identity, issued, authErr = issuer.Pair(hb.PairingCode, hb.MachineID)
			hb.MachineID = identity.Subject
			hb.Credential = issued
		}
	} else {
		identity, authErr = r.L0.Authorize(hb.Credential)
		if identity.Subject != "" && hb.HostID == "" {
			hb.HostID = identity.Subject
		}
	}
	if authErr != nil {
		_ = c.WriteFrame(ctx, wire.Envelope{T: "hello.err", Payload: wire.MustJSON(errBody{Code: "UNAUTHORIZED"})})
		return
	}
	switch hb.Role {
	case "host":
		r.serveHost(ctx, c, hb)
	case "client":
		r.serveClient(ctx, c, hb)
	default:
		_ = c.WriteFrame(ctx, wire.Envelope{T: "hello.err", Payload: wire.MustJSON(errBody{Code: "BAD_ROLE"})})
	}
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

// serveHost runs a host's control connection: registers it, acknowledges
// hello, handles port.expose (transparent), and routes piktak-native frames.
func (r *Relay) serveHost(ctx context.Context, c wire.FrameConn, hb helloBody) {
	hostID := hb.HostID
	if hostID == "" {
		hostID = hb.Code // machine code doubles as host id
	}
	if hostID == "" {
		hostID = fmt.Sprintf("host-%d", atomic.AddUint64(&r.nextID, 1))
	}
	hctx, cancel := context.WithCancel(ctx)
	hs := &hostSession{id: hostID, machineID: hb.MachineID, conn: c, cancel: cancel, dataToken: hasFeature(hb.Features, "data-token")}

	r.mu.Lock()
	if old, dup := r.hosts[hostID]; dup {
		old.cancel()
		_ = old.conn.Close()
	}
	r.hosts[hostID] = hs
	r.mu.Unlock()

	defer func() {
		r.mu.Lock()
		if cur, ok := r.hosts[hostID]; ok && cur == hs {
			delete(r.hosts, hostID)
		}
		r.dropTunnelsForHostLocked(hostID)
		r.dropRawConnsForHostLocked(hostID)
		r.mu.Unlock()
		cancel()
	}()

	_ = c.WriteFrame(ctx, wire.Envelope{T: "hello.ok", Payload: wire.MustJSON(helloOK{HostID: hostID, MachineID: hb.MachineID, Credential: hb.Credential})})

	for {
		env, err := c.ReadFrame(hctx)
		if err != nil {
			return
		}
		switch env.T {
		case "port.expose":
			// Transparent: host declares the local addr it wants exposed
			// behind the ingress port. v1: one ingress routes to one host.
			var pb portBody
			decode(env.Payload, &pb)
			r.mu.Lock()
			hs.local = pb.Local
			r.mu.Unlock()
			_ = c.WriteFrame(ctx, wire.Envelope{T: "port.ok", ID: env.ID, Payload: wire.MustJSON(portOK{Ingress: r.Ingress})})
		case "tun.ok":
			// PIK.TAK-native: host accepted a tunnel; forward to client.
			r.routeToClient(env)
		case "tun.close":
			r.closeTunnelLocked(env.Tunnel, env)
		default:
			// PIK.TAK-native opaque: route by tunnel to the client end.
			if env.Tunnel != "" {
				r.routeToClient(env)
			}
		}
	}
}

// serveClient is the piktak-native client control connection.
func (r *Relay) serveClient(ctx context.Context, c wire.FrameConn, hb helloBody) {
	r.mu.Lock()
	hs, ok := r.hosts[hb.HostID]
	r.mu.Unlock()
	if !ok {
		_ = c.WriteFrame(ctx, wire.Envelope{T: "hello.err", Payload: wire.MustJSON(errBody{Code: "HOST_NOT_FOUND"})})
		return
	}
	cs := &clientSession{conn: c}
	_ = c.WriteFrame(ctx, wire.Envelope{T: "hello.ok", Payload: wire.MustJSON(helloOK{HostID: hb.HostID})})
	defer func() {
		r.mu.Lock()
		r.dropTunnelsForClientLocked(cs)
		r.mu.Unlock()
	}()
	for {
		env, err := c.ReadFrame(ctx)
		if err != nil {
			return
		}
		switch env.T {
		case "tun.open":
			tun := r.newTunnelID()
			env.Tunnel = tun
			r.mu.Lock()
			r.tunnels[tun] = &tunnelSession{host: hs, client: cs}
			r.mu.Unlock()
			_ = hs.conn.WriteFrame(ctx, env)
		case "tun.close":
			r.closeTunnelLocked(env.Tunnel, env)
		default:
			if env.Tunnel != "" {
				r.mu.Lock()
				t := r.tunnels[env.Tunnel]
				r.mu.Unlock()
				if t != nil && t.host != nil {
					_ = t.host.conn.WriteFrame(ctx, env)
				}
			}
		}
	}
}

// --- piktak-native routing helpers ---

func (r *Relay) routeToClient(env wire.Envelope) {
	r.mu.Lock()
	t := r.tunnels[env.Tunnel]
	r.mu.Unlock()
	if t != nil && t.client != nil {
		_ = t.client.conn.WriteFrame(context.Background(), env)
	}
}

func (r *Relay) closeTunnelLocked(tun string, env wire.Envelope) {
	r.mu.Lock()
	t := r.tunnels[tun]
	delete(r.tunnels, tun)
	r.mu.Unlock()
	if t == nil {
		return
	}
	if t.host != nil {
		_ = t.host.conn.WriteFrame(context.Background(), env)
	}
	if t.client != nil {
		_ = t.client.conn.WriteFrame(context.Background(), env)
	}
}

func (r *Relay) dropTunnelsForHostLocked(hostID string) {
	for id, t := range r.tunnels {
		if t.host != nil && t.host.id == hostID {
			delete(r.tunnels, id)
		}
	}
}

func (r *Relay) dropTunnelsForClientLocked(cs *clientSession) {
	for id, t := range r.tunnels {
		if t.client == cs {
			delete(r.tunnels, id)
		}
	}
}

func (r *Relay) dropRawConnsForHostLocked(hostID string) {
	for id, rc := range r.rawConns {
		if rc.host != nil && rc.host.id == hostID {
			_ = rc.browser.Close()
			delete(r.rawConns, id)
		}
	}
}

func (r *Relay) newTunnelID() string {
	return fmt.Sprintf("t%d", atomic.AddUint64(&r.nextTun, 1))
}

func newToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// --- types ---

type hostSession struct {
	id        string
	machineID string
	conn      wire.FrameConn
	local     string
	cancel    context.CancelFunc
	dataToken bool
}

type clientSession struct{ conn wire.FrameConn }

type tunnelSession struct {
	host   *hostSession
	client *clientSession
}

// rawConn is a transparent inbound: a browser connected to the ingress port
// and is waiting for the host to attach a raw data channel.
type rawConn struct {
	id      string
	host    *hostSession
	token   string
	browser net.Conn
	ready   chan struct{}
}

// --- control-frame bodies the relay owns ---
//
// These are the relay's private vocabulary. The host and client drivers
// mirror the same JSON shape; the wire contract is the JSON, not a shared
// struct, so they do not import this package.

type helloBody struct {
	Role        string   `json:"role,omitempty"`
	Protocol    string   `json:"protocol,omitempty"`
	Version     int      `json:"version,omitempty"`
	Code        string   `json:"code,omitempty"`
	Credential  string   `json:"credential,omitempty"`
	PairingCode string   `json:"pairingCode,omitempty"`
	MachineID   string   `json:"machineId,omitempty"`
	HostID      string   `json:"hostId,omitempty"`
	Features    []string `json:"features,omitempty"`
}

type helloOK struct {
	HostID     string `json:"hostId,omitempty"`
	MachineID  string `json:"machineId,omitempty"`
	Credential string `json:"credential,omitempty"`
}

type errBody struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

type portBody struct {
	Local string `json:"local,omitempty"`
}

type portOK struct {
	Ingress string `json:"ingress,omitempty"`
}

type dataBody struct {
	ConnID string `json:"connId,omitempty"`
	Token  string `json:"token,omitempty"`
}

func hasFeature(features []string, want string) bool {
	for _, feature := range features {
		if feature == want {
			return true
		}
	}
	return false
}

func decode(p json.RawMessage, v any) {
	if len(p) > 0 {
		_ = json.Unmarshal(p, v)
	}
}
