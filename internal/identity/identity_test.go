package identity

import (
	"errors"
	"strings"
	"testing"
)

const sample = "---\nschema: 1\nrole: ceo\nkind: soul\nstatus: written\n---\n\nYou are the CEO.\n"

func TestStampVerifyRoundTrip(t *testing.T) {
	rid := NewRoleID()
	key := []byte("k")
	out, err := Stamp([]byte(sample), rid, "soul", key)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.HasPrefix(s, "---\nschema: 1\nrole: ceo\nkind: soul\nstatus: written\nrole_id: "+rid) {
		t.Fatalf("order not preserved:\n%s", s)
	}
	front, body, _, _ := Split(out)
	if err := Verify(front, body, rid, "soul.md", key, true); err != nil {
		t.Fatal(err)
	}
	// Wrong role_id fails loudly.
	if err := Verify(front, body, NewRoleID(), "soul.md", key, true); !errors.Is(err, ErrMismatch) {
		t.Fatalf("wrong role id: %v", err)
	}
	// Edited body without re-stamp fails on hash.
	if err := Verify(front, body+"tampered\n", rid, "soul.md", key, true); !errors.Is(err, ErrMismatch) || !strings.Contains(err.Error(), "content_hash") {
		t.Fatalf("edited body: %v", err)
	}
	// Re-stamped but signed with another key fails HMAC.
	out2, _ := Stamp(append(out, []byte("more\n")...), rid, "soul", []byte("other"))
	f2, b2, _, _ := Split(out2)
	if err := Verify(f2, b2, rid, "soul.md", key, true); !errors.Is(err, ErrMismatch) || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("foreign key: %v", err)
	}
	// Edited-but-unsigned (hash re-stamped, no signature) fails when a keyring exists.
	out3, _ := Stamp(append(out, []byte("more\n")...), rid, "soul", nil)
	f3, b3, _, _ := Split(out3)
	if err := Verify(f3, b3, rid, "soul.md", key, true); !errors.Is(err, ErrMismatch) || !strings.Contains(err.Error(), "unsigned") {
		t.Fatalf("unsigned: %v", err)
	}
	// Same file passes where no keyring is present (fresh clone).
	if err := Verify(f3, b3, rid, "soul.md", nil, false); err != nil {
		t.Fatalf("no keyring should pass hash-only: %v", err)
	}
	// Unstamped scaffold passes.
	f0, b0, _, _ := Split([]byte(sample))
	if err := Verify(f0, b0, rid, "soul.md", nil, false); err != nil {
		t.Fatal(err)
	}
}

func TestKeyring(t *testing.T) {
	home := t.TempDir()
	k, created, err := EnsureKeyring(home)
	if err != nil || !created || len(k.Key) != 32 {
		t.Fatalf("%v %v %v", k, created, err)
	}
	k2, created2, err := EnsureKeyring(home)
	if err != nil || created2 || string(k2.Key) != string(k.Key) {
		t.Fatal("keyring not stable")
	}
}
