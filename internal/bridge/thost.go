// Package thost is the transparent host driver. It dials the relay, pairs with
// a machine code, exposes a local TCP address (for example a loopback dsh web
// server) behind the relay's ingress port, and for each inbound browser
// connection dials a raw data channel back to the relay and splices it to the
// local address. The relay shuttles bytes between browser and local target
// without parsing them, so dsh and the browser are unaware of PIK.TAK.
//
// This driver imports only wire and the standard library — it never imports
// l2, l3, or the relay. It is the transparent counterpart to internal/host
// (which runs a structured piktak-native adapter).
package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"

	"github.com/charlzyx/piktak/internal/status"
	"github.com/charlzyx/piktak/internal/wire"
)

// Host is configured by the binary: where the relay's wire listener is, the
// machine code (pairing secret + host identity), and the local address to
// expose behind the relay ingress.
type Host struct {
	Addr   string
	Code   string
	HostID string // optional; defaults to Code
	Local  string // local addr to expose, e.g. 127.0.0.1:7531
	Status *status.State
}

// Run dials the relay, pairs, exposes the local port, and serves inbound
// connections until ctx is cancelled or the control connection drops.
func (h *Host) Run(ctx context.Context) error {
	c, err := wire.DialTCP(ctx, h.Addr)
	if err != nil {
		return fmt.Errorf("thost: dial: %w", err)
	}
	defer c.Close()

	hostID := h.HostID
	if hostID == "" {
		hostID = h.Code
	}
	if err := c.WriteFrame(ctx, wire.Envelope{
		T: "hello",
		Payload: wire.MustJSON(helloBody{
			Role:     "host",
			Protocol: wire.ProtocolTCP,
			Version:  wire.Version,
			Code:     h.Code,
			HostID:   hostID,
		}),
	}); err != nil {
		return fmt.Errorf("thost: hello: %w", err)
	}
	ok, err := c.ReadFrame(ctx)
	if err != nil {
		return fmt.Errorf("thost: hello read: %w", err)
	}
	if ok.T != "hello.ok" {
		return fmt.Errorf("thost: hello rejected: %s", ok.T)
	}
	log.Printf("thost online: id=%s local=%s", hostID, h.Local)
	if h.Status != nil {
		h.Status.SetConnected(true)
		defer h.Status.SetConnected(false)
	}

	if err := c.WriteFrame(ctx, wire.Envelope{
		T:       "port.expose",
		Payload: wire.MustJSON(portBody{Local: h.Local}),
	}); err != nil {
		return fmt.Errorf("thost: port.expose: %w", err)
	}
	pok, err := c.ReadFrame(ctx)
	if err != nil {
		return fmt.Errorf("thost: port.ok read: %w", err)
	}
	if pok.T != "port.ok" {
		return fmt.Errorf("thost: port.expose rejected: %s", pok.T)
	}
	var pk portOK
	decode(pok.Payload, &pk)
	log.Printf("thost exposed: ingress=%s -> local=%s", pk.Ingress, h.Local)

	for {
		env, err := c.ReadFrame(ctx)
		if err != nil {
			return fmt.Errorf("thost: read: %w", err)
		}
		if env.T != "inbound" {
			continue
		}
		var db dataBody
		decode(env.Payload, &db)
		if db.ConnID == "" {
			continue
		}
		go h.handleInbound(ctx, db.ConnID)
	}
}

// handleInbound opens a raw data channel for one browser connection and
// splices it to the local target. Each inbound gets its own data connection.
func (h *Host) handleInbound(ctx context.Context, connID string) {
	dc, err := wire.DialTCP(ctx, h.Addr)
	if err != nil {
		log.Printf("thost: data dial: %v", err)
		return
	}
	defer dc.Close()
	if err := dc.WriteFrame(ctx, wire.Envelope{
		T:       "data.attach",
		Payload: wire.MustJSON(dataBody{ConnID: connID}),
	}); err != nil {
		log.Printf("thost: data.attach: %v", err)
		return
	}
	ack, err := dc.ReadFrame(ctx)
	if err != nil {
		log.Printf("thost: data.ack read: %v", err)
		return
	}
	if ack.T != "data.ack" {
		log.Printf("thost: data.attach rejected: %s", ack.T)
		return
	}

	local, err := net.Dial("tcp", h.Local)
	if err != nil {
		log.Printf("thost: local dial %s: %v", h.Local, err)
		return
	}
	defer local.Close()

	rawNet, br := dc.Raw()
	log.Printf("thost inbound connID=%s data-conn local=%s remote=%s -> local=%s",
		connID, rawNet.LocalAddr(), rawNet.RemoteAddr(), h.Local)
	if h.Status != nil {
		h.Status.AddRequest()
	}
	spliceBoth(local, rawNet, br)
}

// spliceBoth copies bytes both ways between the local target and the relay
// data channel using TCP half-close. When one direction's read hits EOF it
// CloseWrites the other side (not full-close): a client that sends a request
// and half-closes (e.g. Connection: close) must still receive the server's
// full response before the socket is torn down. Only after BOTH directions are
// done are the sockets fully closed.
func spliceBoth(local net.Conn, dataNet net.Conn, dataReader io.Reader) {
	done := make(chan struct{}, 2)
	go func() {
		n, err := io.Copy(dataNet, local) // local(dsh) -> relay -> browser (response)
		closeWrite(dataNet)
		log.Printf("thost splice: dsh->relay EOF bytes=%d err=%v", n, err)
		done <- struct{}{}
	}()
	go func() {
		n, err := io.Copy(local, dataReader) // browser -> relay -> local(dsh) (request)
		closeWrite(local)
		log.Printf("thost splice: relay->dsh EOF bytes=%d err=%v", n, err)
		done <- struct{}{}
	}()
	<-done
	<-done
	local.Close()
	dataNet.Close()
	log.Printf("thost splice: closed both")
}

// closeWrite half-closes the write side of a TCP conn so the peer sees EOF
// without tearing down the socket. No-op for non-TCP writers.
func closeWrite(c any) {
	if w, ok := c.(interface{ CloseWrite() error }); ok {
		_ = w.CloseWrite()
	}
}

// Control-frame bodies mirror the relay's private vocabulary; the wire contract
// is the JSON shape, so thost does not import relay.
type helloBody struct {
	Role     string `json:"role,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Version  int    `json:"version,omitempty"`
	Code     string `json:"code,omitempty"`
	HostID   string `json:"hostId,omitempty"`
}

type portBody struct {
	Local string `json:"local,omitempty"`
}

type portOK struct {
	Ingress string `json:"ingress,omitempty"`
}

type dataBody struct {
	ConnID string `json:"connId,omitempty"`
}

func decode(p json.RawMessage, v any) {
	if len(p) > 0 {
		_ = json.Unmarshal(p, v)
	}
}
