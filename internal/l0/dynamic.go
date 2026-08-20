package l0

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// CredentialIssuer is an optional extension used by the Go Relay. It keeps the
// basic Pairer interface compatible while allowing one-time pairing to mint a
// stable credential for later reconnects.
type CredentialIssuer interface {
	Pair(token, requestedID string) (Identity, string, error)
}

// DynamicPairer supports one-time pairing codes and persistent machine
// credentials. It is intentionally Go-only; the Cloudflare Worker has its own
// pairing implementation and does not use this type.
type DynamicPairer struct {
	mu          sync.Mutex
	pairingHash string
	machines    map[string]string // machine ID -> credential hash
	statePath   string
}

type dynamicState struct {
	PairingHash string            `json:"pairing_hash"`
	Machines    map[string]string `json:"machines"`
}

// GeneratePairingCode returns a one-time bootstrap code suitable for the Go
// Relay operator to pass to a Machine out of band.
func GeneratePairingCode() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// WritePairingCode atomically updates the pending pairing code while retaining
// already paired Machines. The file is created mode 0600.
func WritePairingCode(path, code string) error {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(code) == "" {
		return errors.New("l0: state path and pairing code are required")
	}
	var s dynamicState
	if b, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(b, &s); err != nil {
			return fmt.Errorf("l0: parse pairing state: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("l0: read pairing state: %w", err)
	}
	if s.Machines == nil {
		s.Machines = map[string]string{}
	}
	s.PairingHash = hashSecret(code)
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func NewDynamicPairer(pairingCode, statePath string) (*DynamicPairer, error) {
	if strings.TrimSpace(pairingCode) == "" && statePath == "" {
		return nil, errors.New("l0: pairing code or state path is required")
	}
	p := &DynamicPairer{machines: map[string]string{}, statePath: statePath}
	if strings.TrimSpace(pairingCode) != "" {
		p.pairingHash = hashSecret(pairingCode)
	}
	if statePath == "" {
		return p, nil
	}
	b, err := os.ReadFile(statePath)
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return nil, fmt.Errorf("l0: read pairing state: %w", err)
	}
	var s dynamicState
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("l0: parse pairing state: %w", err)
	}
	// An existing state file is authoritative; an empty pairing hash means the
	// one-time code has already been consumed.
	p.pairingHash = s.PairingHash
	for id, h := range s.Machines {
		p.machines[id] = h
	}
	return p, nil
}

func (p *DynamicPairer) Authorize(token string) (Identity, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	h := hashSecret(token)
	for id, credentialHash := range p.machines {
		if subtleEqual(h, credentialHash) {
			return Identity{Subject: id}, nil
		}
	}
	return Identity{}, ErrUnauthorized
}

// Pair consumes the configured pairing code and returns a persistent machine
// credential. A supplied machine ID is retained; otherwise one is generated.
func (p *DynamicPairer) Pair(token, requestedID string) (Identity, string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !subtleEqual(hashSecret(token), p.pairingHash) {
		return Identity{}, "", ErrUnauthorized
	}
	id := strings.TrimSpace(requestedID)
	if id == "" {
		id = randomID("machine-")
	}
	if _, exists := p.machines[id]; exists {
		return Identity{}, "", errors.New("l0: machine id already paired")
	}
	credential := randomID("ptk-")
	p.machines[id] = hashSecret(credential)
	p.pairingHash = ""
	if err := p.persistLocked(); err != nil {
		delete(p.machines, id)
		return Identity{}, "", err
	}
	return Identity{Subject: id}, credential, nil
}

func (p *DynamicPairer) persistLocked() error {
	if p.statePath == "" {
		return nil
	}
	b, err := json.MarshalIndent(dynamicState{PairingHash: p.pairingHash, Machines: p.machines}, "", "  ")
	if err != nil {
		return err
	}
	tmp := p.statePath + ".tmp"
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, p.statePath)
}

func hashSecret(s string) string   { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func subtleEqual(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func randomID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s%d", prefix, time.Now().UnixNano())
	}
	return prefix + hex.EncodeToString(b[:])
}
