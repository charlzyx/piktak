package relay

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync/atomic"
	"time"

	"github.com/charlzyx/piktak/internal/wire"
)

func (r *Relay) acceptIngress(ctx context.Context, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("ingress accept: %v", err)
			return
		}
		go r.serveIngress(ctx, c)
	}
}

// serveIngress handles a raw inbound connection — typically a browser hitting
// the public ingress port. It finds the host that exposed a local port, tells
// it an inbound arrived, and waits for the host to attach a raw data channel.
// The byte splice itself happens in serveData; this function never reads or
// parses the browser's bytes. v1 routes ingress to the single host that has
// called port.expose; multi-host routing (SNI/path) is later work.
func (r *Relay) serveIngress(ctx context.Context, browser net.Conn) {
	r.mu.Lock()
	var hs *hostSession
	for _, h := range r.hosts {
		if h.local != "" {
			hs = h
			break
		}
	}
	r.mu.Unlock()
	if hs == nil {
		// No host has exposed a local port yet; drop the inbound.
		browser.Close()
		return
	}

	connID := r.newConnID()
	log.Printf("inbound accepted connID=%s remote=%s", connID, browser.RemoteAddr())
	rc := &rawConn{id: connID, host: hs, browser: browser, ready: make(chan struct{})}
	r.mu.Lock()
	r.rawConns[connID] = rc
	r.mu.Unlock()

	if err := hs.conn.WriteFrame(ctx, wire.Envelope{
		T:       "inbound",
		Payload: wire.MustJSON(dataBody{ConnID: connID}),
	}); err != nil {
		r.mu.Lock()
		delete(r.rawConns, connID)
		r.mu.Unlock()
		browser.Close()
		return
	}

	select {
	case <-rc.ready:
		// Host attached; serveData now owns the browser conn and closes it.
		return
	case <-time.After(15 * time.Second):
		r.mu.Lock()
		delete(r.rawConns, connID)
		r.mu.Unlock()
		browser.Close()
	case <-ctx.Done():
		r.mu.Lock()
		delete(r.rawConns, connID)
		r.mu.Unlock()
		browser.Close()
	}
}

func (r *Relay) newConnID() string {
	return fmt.Sprintf("c%d", atomic.AddUint64(&r.nextConn, 1))
}
