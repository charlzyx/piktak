// Package l2 is the capability-negotiation layer.
//
// Once L1 has paired a tunnel, the host adapter announces what protocol runs
// inside it (adapter name + version + capabilities). The client accepts or
// rejects. The relay forwards these frames opaquely and never decodes them;
// only the two peers of a tunnel do. l2 is a leaf types package: it depends
// only on the standard library.
package l2

import "encoding/json"

// Negotiate is sent by the host adapter right after a tunnel is accepted.
type Negotiate struct {
	Adapter string   `json:"adapter"`
	Version int      `json:"version"`
	Caps    []string `json:"caps,omitempty"`
}

// Ack is the client's answer to a Negotiate.
type Ack struct {
	OK      bool `json:"ok"`
	Version int  `json:"version,omitempty"`
}

// Error is sent in place of (or after) an Ack when negotiation or an adapter
// frame fails. Stable codes live at the relay layer; adapters define their
// own L3 error shapes.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

// Marshal returns the JSON RawMessage form, for placing in a wire.Envelope
// Payload. Convenience; callers may also json.Marshal directly.
func (n Negotiate) Marshal() json.RawMessage { b, _ := json.Marshal(n); return b }
func (a Ack) Marshal() json.RawMessage       { b, _ := json.Marshal(a); return b }
func (e Error) Marshal() json.RawMessage     { b, _ := json.Marshal(e); return b }
