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
