package relay

import (
	"context"
	"log"

	"github.com/charlzyx/piktak/internal/wire"
)

// serveData handles a host's raw data channel. The host dials the wire
// listener, sends one data.attach frame naming a connId, then the connection
// becomes raw bytes. The relay acks, claims the matching inbound (signalling
// the waiting ingress handler to hand off the browser socket), and splices the
// browser's socket to this data channel byte for byte. The relay inspects no
// bytes — HTTP, WebSocket, anything flows through untouched.
func (r *Relay) serveData(ctx context.Context, c *wire.TCPConn, first wire.Envelope) {
	var db dataBody
	decode(first.Payload, &db)
	connID := db.ConnID

	_ = c.WriteFrame(ctx, wire.Envelope{
		T:       "data.ack",
		Payload: wire.MustJSON(dataBody{ConnID: connID}),
	})

	r.mu.Lock()
	rc := r.rawConns[connID]
	if rc != nil {
		// Claim it so the ingress timeout path can't double-clean.
		delete(r.rawConns, connID)
	}
	r.mu.Unlock()
	if rc == nil {
		// No matching inbound (timed out, unknown, or duplicate); drop.
		return
	}

	rawNet, br := c.Raw()
	close(rc.ready) // hand the browser socket off to this splice.
	log.Printf("data attached connID=%s data-conn local=%s remote=%s browser=%s",
		connID, rawNet.LocalAddr(), rawNet.RemoteAddr(), rc.browser.RemoteAddr())

	splice(connID, rc.browser, br, rawNet)
}
