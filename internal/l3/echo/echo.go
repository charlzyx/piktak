// Package echo is the reference L3 adapter. It speaks one frame type, "echo":
// the client sends {"t":"echo","p":{"text":"..."}}, the host replies
// {"t":"echoed","p":{"text":"..."}} with the same id. It exists to prove that
// L1 pairing + L2 negotiation + L3 forwarding close the loop end to end.
package echo

import (
	"context"
	"encoding/json"

	"github.com/charlzyx/piktak/internal/wire"
)

// Adapter implements l3.Adapter.
type Adapter struct{}

func (Adapter) Name() string   { return "echo" }
func (Adapter) Version() int   { return 1 }
func (Adapter) Caps() []string { return []string{"echo"} }

type in struct {
	Text string `json:"text"`
}
type out struct {
	Text string `json:"text"`
}

func (Adapter) Handle(ctx context.Context, env wire.Envelope, send func(env wire.Envelope) error) error {
	_ = ctx
	if env.T != "echo" {
		// Unknown frame; echo adapter ignores anything it does not speak.
		return nil
	}
	var p in
	if len(env.Payload) > 0 {
		_ = json.Unmarshal(env.Payload, &p)
	}
	return send(wire.Envelope{
		T:       "echoed",
		ID:      env.ID,
		Payload: wire.MustJSON(out{Text: p.Text}),
	})
}
