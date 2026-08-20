// Package l0 is the identity & pairing layer (the discovery plane).
//
// The broker calls Authorize to decide whether a connecting party may use the
// relay, and to learn a Subject it can attribute the connection to. Pairing is
// pluggable: v0 ships PSK and machine-code; mTLS and invite-token are later
// strategies that satisfy the same interface without touching L1.
package l0

import (
	"crypto/subtle"
	"errors"
	"strings"
)

// Pairer authorizes a connecting party given the token/code it presented in
// hello. The relay calls this on every hello; strategies decide what counts as
// proof.
type Pairer interface {
	Authorize(token string) (Identity, error)
}

// Identity is what L0 hands back on success. Subject is the stable label L1
// routes/logs by; empty means "admitted but anonymous" (e.g. PSK, where the
// relay trusts the secret, not a subject claim).
type Identity struct {
	Subject string
}

// ErrUnauthorized is returned by Authorize when the token is rejected.
var ErrUnauthorized = errors.New("l0: unauthorized")

// PSK is a pre-shared-secret pairer: any party presenting the configured
// secret is admitted. The host id is declared in the hello frame and trusted
// on the strength of the shared secret.
type PSK struct {
	Secret []byte
}

func (p PSK) Authorize(token string) (Identity, error) {
	if subtle.ConstantTimeCompare([]byte(token), p.Secret) != 1 {
		return Identity{}, ErrUnauthorized
	}
	return Identity{}, nil
}

// MachineCode is a machine-code pairer: a short, human-typeable code is the
// pairing secret, one per host. Codes are exchanged out of band — the operator
// generates 8 digits, adds them to the relay's allowlist, and types the same
// code into the host. The host id is derived from the code (or declared in
// hello), so the code doubles as discovery identity.
//
// This is the "remote discovery" half of the relay: the code is what a client
// uses to name the host it wants to reach.
type MachineCode struct {
	Codes map[string]struct{}
}

// Authorize admits a party presenting a known code. The Subject returned is
// the code itself, so the relay can use it as the host id.
func (m MachineCode) Authorize(code string) (Identity, error) {
	if _, ok := m.Codes[code]; !ok {
		return Identity{}, ErrUnauthorized
	}
	return Identity{Subject: code}, nil
}

// NewMachineCode builds a MachineCode pairer from a list of codes. Blank and
// duplicate entries are ignored; surrounding whitespace is trimmed.
func NewMachineCode(codes ...string) MachineCode {
	set := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		c = strings.TrimSpace(c)
		if c != "" {
			set[c] = struct{}{}
		}
	}
	return MachineCode{Codes: set}
}
