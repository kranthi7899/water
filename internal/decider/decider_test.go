package decider

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestNullDecideAlwaysReturnsErrUnavailable(t *testing.T) {
	var n Null
	got, err := n.Decide(context.Background(), Request{Kind: Choice, State: "anything", Options: []string{"a", "b"}})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Null.Decide error = %v, want ErrUnavailable", err)
	}
	if got != (Answer{}) {
		t.Fatalf("Null.Decide answer = %+v, want the zero value", got)
	}
}

func TestNewNoneReturnsWorkingNull(t *testing.T) {
	d, err := New("none")
	if err != nil {
		t.Fatalf("New(%q) error = %v, want nil", "none", err)
	}
	if _, ok := d.(Null); !ok {
		t.Fatalf("New(%q) = %T, want Null", "none", d)
	}
	if _, err := d.Decide(context.Background(), Request{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("New(%q) decider's Decide error = %v, want ErrUnavailable", "none", err)
	}
}

func TestNewUnsupportedProviderErrorsNamesIt(t *testing.T) {
	d, err := New("jev")
	if err == nil {
		t.Fatal("New(\"jev\") error = nil, want an error naming the unsupported provider")
	}
	if d != nil {
		t.Fatalf("New(\"jev\") decider = %v, want nil", d)
	}
	if !strings.Contains(err.Error(), "jev") {
		t.Fatalf("New(\"jev\") error = %q, want it to name the provider", err.Error())
	}
}
