package auth

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"water/internal/backend"
)

func TestPlanAndLogin(t *testing.T) {
	p, err := Plan(backend.ClaudeSubscriptionName, false)
	if err != nil || strings.Join(p.Command, " ") != "claude login" {
		t.Fatalf("%v %v", p, err)
	}
	p, _ = Plan(backend.ClaudeSubscriptionName, true)
	if strings.Join(p.Command, " ") != "claude setup-token" || !p.Headless {
		t.Fatalf("%v", p)
	}
	p, _ = Plan(backend.CodexSubscriptionName, true)
	if !strings.Contains(strings.Join(p.Command, " "), "device") {
		t.Fatalf("%v", p)
	}
	if _, err := Plan("api", false); err == nil {
		t.Fatal("api has no login flow")
	}
	// A fake `claude` on PATH; the runner records what would have run.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	t.Setenv("PATH", dir)
	var ran []string
	run := func(_ context.Context, name string, args ...string) error {
		ran = append(ran, name+" "+strings.Join(args, " "))
		return nil
	}
	if _, err := Login(context.Background(), run, backend.ClaudeSubscriptionName, false); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || ran[0] != "claude login" {
		t.Fatalf("ran %v", ran)
	}
	if _, err := Login(context.Background(), run, backend.CodexSubscriptionName, false); err == nil {
		t.Fatal("codex not installed should fail")
	}
}

func TestVerifyRoundTripGatesSuccess(t *testing.T) {
	ok := backend.NewFake("sub")
	ok.Reply = func(backend.Request) string { return "OK" }
	if _, err := VerifyRoundTrip(context.Background(), ok, 0); err != nil {
		t.Fatal(err)
	}
	bad := backend.NewFake("sub")
	bad.FailWith = errors.New("not logged in")
	if _, err := VerifyRoundTrip(context.Background(), bad, 0); !errors.Is(err, ErrRoundTripFailed) {
		t.Fatalf("failed round trip must not verify: %v", err)
	}
	empty := backend.NewFake("sub")
	empty.Reply = func(backend.Request) string { return "   " }
	if _, err := VerifyRoundTrip(context.Background(), empty, 0); !errors.Is(err, ErrRoundTripFailed) {
		t.Fatal("empty reply must not verify")
	}
	metered := backend.NewFake("api")
	metered.Avail.Metered = true
	metered.Reply = func(backend.Request) string { return "OK" }
	if _, err := VerifyRoundTrip(context.Background(), metered, 0); !errors.Is(err, ErrRoundTripFailed) {
		t.Fatal("metered reply must not verify a subscription setup")
	}
}
