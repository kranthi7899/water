package vault

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestSecretNeverRenders(t *testing.T) {
	s := NewSecret("hunter2")
	wrapped := struct {
		Token Secret
		Ptr   *Secret
	}{s, &s}
	j, _ := json.Marshal(wrapped)
	for _, out := range []string{
		fmt.Sprint(s), fmt.Sprintf("%v %+v %#v %s %q %x", s, s, s, s, s, s),
		fmt.Sprintf("%v %+v %#v", wrapped, wrapped, wrapped), string(j),
	} {
		if strings.Contains(out, "hunter2") {
			t.Fatalf("secret rendered: %s", out)
		}
	}
	if s.Reveal() != "hunter2" {
		t.Fatal("reveal")
	}
}

func TestMemoryVault(t *testing.T) {
	v := NewMemory()
	if _, err := v.Get("svc", "acct"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	v.Set("svc", "acct", NewSecret("x"))
	if got, err := v.Get("svc", "acct"); err != nil || got.Reveal() != "x" {
		t.Fatal(err)
	}
	if err := v.Delete("svc", "acct"); err != nil {
		t.Fatal(err)
	}
	if err := v.Delete("svc", "acct"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := v.Set("", "a", NewSecret("x")); err == nil {
		t.Fatal("empty service accepted")
	}
}
