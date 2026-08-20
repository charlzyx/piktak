package l0

import (
	"path/filepath"
	"testing"
)

func TestDynamicPairerConsumesCodeAndPersistsCredential(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state.json")
	p, err := NewDynamicPairer("pair-me", state)
	if err != nil {
		t.Fatal(err)
	}
	id, cred, err := p.Pair("pair-me", "")
	if err != nil {
		t.Fatal(err)
	}
	if id.Subject == "" || cred == "" {
		t.Fatal("pair did not issue identity and credential")
	}
	if _, _, err := p.Pair("pair-me", ""); err == nil {
		t.Fatal("pairing code reused")
	}
	got, err := p.Authorize(cred)
	if err != nil || got.Subject != id.Subject {
		t.Fatalf("authorize credential: %v %+v", err, got)
	}
	p2, err := NewDynamicPairer("ignored", state)
	if err != nil {
		t.Fatal(err)
	}
	got, err = p2.Authorize(cred)
	if err != nil || got.Subject != id.Subject {
		t.Fatalf("authorize after reload: %v %+v", err, got)
	}
}
