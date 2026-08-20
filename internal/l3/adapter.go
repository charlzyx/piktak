// Package l3 is the application-protocol layer.
//
// An Adapter owns the vocabulary spoken inside a tunnel after L2 negotiation.
// The broker NEVER imports this package (enforced by import graph: internal/relay
// imports only wire and l0). A host selects one adapter; a client must
// understand the same adapter name + version.
//
// v0 ships the echo adapter (internal/l3/echo) to prove frames flow end to end.
package l3

import (
	"context"

	"github.com/charlzyx/piktak/internal/wire"
)

// Adapter is an application protocol running inside a tunnel.
type Adapter interface {
	Name() string
	Version() int
	Caps() []string
	// Handle processes one inbound client frame and may emit zero or more
	// replies via send. Replies already carry the tunnel id (the host driver
	// stamps it); the adapter should not set it. Returning an error causes the
	// host driver to send an l2.Error to the client.
	Handle(ctx context.Context, in wire.Envelope, send func(env wire.Envelope) error) error
}

// Registry maps adapter name -> Adapter. The host driver looks its adapter up
// by the name declared during L2.
type Registry struct {
	adapters map[string]Adapter
}

func NewRegistry() *Registry { return &Registry{adapters: map[string]Adapter{}} }

func (r *Registry) Register(a Adapter) { r.adapters[a.Name()] = a }

func (r *Registry) Get(name string) (Adapter, bool) {
	a, ok := r.adapters[name]
	return a, ok
}
