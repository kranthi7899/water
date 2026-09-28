package gate_test

import (
	"context"
	"testing"
	"time"

	"water/internal/connectors"
	"water/internal/connectors/fake"
	"water/internal/gate"
	"water/internal/twins"
	"water/internal/vault"
)

// TestRateAndUsageCapsSurviveARestart proves the acceptance test literally: a
// second Gate built over the SAME store (a daemon restart reopens the store
// but starts with no in-process rate map) still sees the hits the first Gate
// recorded.
func TestRateAndUsageCapsSurviveARestart(t *testing.T) {
	h := newHarness(t, testManifest)
	ctx := context.Background()
	call := gate.Call{Function: "fake_mail.list_messages", Origin: gate.P0, Taint: gate.Clean}
	for i := 0; i < 2; i++ {
		if _, err := h.g.Invoke(ctx, call); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.g.Invoke(ctx, call); err == nil {
		t.Fatal("expected the rate cap to be reached before restart")
	}
	for i := 0; i < 3; i++ {
		if err := h.g.ModelCall(gate.P0); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.g.ModelCall(gate.P0); err == nil {
		t.Fatal("expected the model-call cap (3 per window) to be reached before restart")
	}

	// Simulate a daemon restart: a brand new Gate over the same manifest,
	// registry shape and store, sharing no in-memory state with h.g at all.
	m, err := twins.Parse([]byte(testManifest))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := connectors.NewRegistry(h.cal, h.mail, fake.NewDocs(), h.notes)
	if err != nil {
		t.Fatal(err)
	}
	v := vault.NewMemory()
	v.Set(fake.MailService, fake.MailAccount, vault.NewSecret("tok-123"))
	g2, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: h.q, Audit: h.log, Vault: v, Store: h.st, Now: func() time.Time { return h.now }})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := g2.Invoke(ctx, call); err == nil {
		t.Fatal("rate cap did not survive the restart")
	}
	if err := g2.ModelCall(gate.P0); err == nil {
		t.Fatal("model-call cap did not survive the restart")
	}
}

// TestStoreBackedCapsPruneExpiredHits proves rate_hits stays bounded: hits
// that have left a key's window are deleted as the gate keeps charging that
// key, while the count inside the window stays exact.
func TestStoreBackedCapsPruneExpiredHits(t *testing.T) {
	h := newHarness(t, testManifest)
	ctx := context.Background()
	epoch := time.Unix(0, 0)
	count := func(key string) int {
		t.Helper()
		n, err := h.st.CountHitsSince(ctx, key, epoch)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	call := gate.Call{Function: "fake_mail.list_messages", Origin: gate.P0, Taint: gate.Clean}
	for i := 0; i < 2; i++ {
		if _, err := h.g.Invoke(ctx, call); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		if err := h.g.ModelCall(gate.P0); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.g.ModelCall(gate.P2); err == nil {
		t.Fatal("expected the model-call cap to be reached")
	}

	// Two windows later every earlier hit has expired.
	h.now = h.now.Add(2 * time.Hour)
	if _, err := h.g.Invoke(ctx, call); err != nil {
		t.Fatal(err)
	}
	if err := h.g.ModelCall(gate.P2); err != nil {
		t.Fatal(err)
	}
	if n := count("fake_mail.list_messages"); n != 1 {
		t.Fatalf("rate_hits rows for the function = %d, want 1: expired hits must be pruned", n)
	}
	if n := count("model"); n != 1 {
		t.Fatalf("rate_hits rows for model = %d, want 1: expired hits must be pruned", n)
	}
	if n := count("model:auto"); n != 1 {
		t.Fatalf("rate_hits rows for model:auto = %d, want 1", n)
	}
	// The in-window count is still exact: one more call fits (max 2), then
	// the cap holds.
	if _, err := h.g.Invoke(ctx, call); err != nil {
		t.Fatalf("second in-window call refused: %v", err)
	}
	if _, err := h.g.Invoke(ctx, call); err == nil {
		t.Fatal("rate cap no longer enforced after pruning")
	}
}
