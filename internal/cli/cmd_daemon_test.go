package cli

import (
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"water/internal/config"
	"water/internal/nervous"
	"water/internal/nervous/sidecar"
)

// TestBuildNervousConfigMatchesDefaultConfig is R-25's drift test
// (docs/slices/R.md's own task-list entry for this task): a Resolved config
// built from nothing but internal/config's own defaults must map, through
// buildNervousConfig, onto exactly what nervous.DefaultConfig() already
// documents as Design §17's defaults — so a daemon that has never touched
// config.yaml behaves identically before and after this task.
func TestBuildNervousConfigMatchesDefaultConfig(t *testing.T) {
	t.Setenv("WATER_HOME", t.TempDir())
	resolved, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	got := buildNervousConfig(resolved)
	want := nervous.DefaultConfig()
	// Registry/Style/Turns/etc. are wired separately in runDaemon (not by
	// buildNervousConfig), and both sides leave them zero here — compare
	// only the fields Design §17 actually gives a config key.
	if got.Tier0Enabled != want.Tier0Enabled {
		t.Errorf("Tier0Enabled = %v, want %v", got.Tier0Enabled, want.Tier0Enabled)
	}
	if got.MainEnabled != want.MainEnabled {
		t.Errorf("MainEnabled = %v, want %v", got.MainEnabled, want.MainEnabled)
	}
	if got.Breaker != want.Breaker {
		t.Errorf("Breaker = %+v, want %+v", got.Breaker, want.Breaker)
	}
	if got.MissWindow != want.MissWindow {
		t.Errorf("MissWindow = %v, want %v", got.MissWindow, want.MissWindow)
	}
	if got.Retention != want.Retention {
		t.Errorf("Retention = %v, want %v", got.Retention, want.Retention)
	}
	if got.AckAfter != want.AckAfter {
		t.Errorf("AckAfter = %v, want %v", got.AckAfter, want.AckAfter)
	}
	if got.Speculation != want.Speculation {
		t.Errorf("Speculation = %v, want %v", got.Speculation, want.Speculation)
	}
	if got.VoiceApprove.Enabled != want.VoiceApprove.Enabled || got.VoiceApprove.Window != want.VoiceApprove.Window {
		t.Errorf("VoiceApprove = %+v, want %+v", got.VoiceApprove, want.VoiceApprove)
	}
	if !reflect.DeepEqual(got.VoiceApprove.InternalDomains, want.VoiceApprove.InternalDomains) {
		t.Errorf("VoiceApprove.InternalDomains = %v, want %v", got.VoiceApprove.InternalDomains, want.VoiceApprove.InternalDomains)
	}
	// PromotionConfig has no code default worth comparing against (R-23's
	// own DefaultDemoteMinSamples/DefaultDemoteMissRatePct constants), but
	// the resolved config's own defaults (10, 20) must still come through.
	if got.Promotion.DemoteMinSamples != nervous.DefaultDemoteMinSamples {
		t.Errorf("Promotion.DemoteMinSamples = %d, want %d", got.Promotion.DemoteMinSamples, nervous.DefaultDemoteMinSamples)
	}
	if got.Promotion.DemoteMissRatePct != nervous.DefaultDemoteMissRatePct {
		t.Errorf("Promotion.DemoteMissRatePct = %d, want %d", got.Promotion.DemoteMissRatePct, nervous.DefaultDemoteMissRatePct)
	}
}

// TestBuildNervousConfigWiresNonDefaultValues is the regression test R-25's
// own brief asks for: a resolved config with non-default values for a
// spread of router.* keys must produce a nervous.Config carrying those
// exact values, not the hardcoded defaults buildNervousConfig starts
// from — proving the wiring is live, not just that the config keys parse
// (internal/config's own tests already cover parsing).
func TestBuildNervousConfigWiresNonDefaultValues(t *testing.T) {
	t.Setenv("WATER_HOME", t.TempDir())
	resolved, err := config.Load(map[string]string{
		"router.tier0.enabled":                  "false",
		"router.main.enabled":                   "false",
		"router.ack_ms":                         "100",
		"router.breaker.failures":               "3",
		"router.breaker.cooldown_seconds":       "15",
		"router.breaker.max_miss_rate_pct":      "10",
		"router.possible_miss_window_seconds":   "30",
		"router.log_retention_days":             "45",
		"router.speculation.enabled":            "false",
		"router.voice_approve.enabled":          "true",
		"router.voice_approve.window_seconds":   "45",
		"router.voice_approve.internal_domains": "a.com, b.com",
		"router.promotion.demote_min_samples":   "7",
		"router.promotion.demote_miss_rate_pct": "15",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := buildNervousConfig(resolved)

	if got.Tier0Enabled {
		t.Error("Tier0Enabled should be false")
	}
	if got.MainEnabled {
		t.Error("MainEnabled should be false")
	}
	if got.AckAfter != 100*time.Millisecond {
		t.Errorf("AckAfter = %v, want 100ms", got.AckAfter)
	}
	wantBreaker := nervous.BreakerConfig{
		Failures: 3, Cooldown: 15 * time.Second, MaxMissRatePct: 10,
		// Not settable keys: keep the code defaults.
		MissSample: nervous.DefaultBreakerConfig().MissSample, MinMissSamples: nervous.DefaultBreakerConfig().MinMissSamples,
	}
	if got.Breaker != wantBreaker {
		t.Errorf("Breaker = %+v, want %+v", got.Breaker, wantBreaker)
	}
	if got.MissWindow != 30*time.Second {
		t.Errorf("MissWindow = %v, want 30s", got.MissWindow)
	}
	if got.Retention != 45*24*time.Hour {
		t.Errorf("Retention = %v, want 45 days", got.Retention)
	}
	if got.Speculation {
		t.Error("Speculation should be false")
	}
	if !got.VoiceApprove.Enabled || got.VoiceApprove.Window != 45*time.Second {
		t.Errorf("VoiceApprove = %+v, want enabled with a 45s window", got.VoiceApprove)
	}
	if want := []string{"a.com", "b.com"}; !reflect.DeepEqual(got.VoiceApprove.InternalDomains, want) {
		t.Errorf("VoiceApprove.InternalDomains = %v, want %v", got.VoiceApprove.InternalDomains, want)
	}
	if got.Promotion.DemoteMinSamples != 7 || got.Promotion.DemoteMissRatePct != 15 {
		t.Errorf("Promotion = %+v, want DemoteMinSamples=7 DemoteMissRatePct=15", got.Promotion)
	}
}

// TestTier1GateReadyRefusesMissingRecord is R-25's own required test: with
// router.tier1.enabled=true but no eval-gate record on disk (the common
// case — nobody has ever run `water route eval --tier1`), the real
// sidecar.ReadEvalRecord/tier1GateReady chain this task wires into
// runDaemon must refuse to start Tier 1, with reason "eval_missing" — the
// same reason GET /v1/router documents for this case. No daemon, no real
// sidecar subprocess: this exercises exactly the two functions
// runDaemon calls before ever constructing a sidecar.Supervisor.
func TestTier1GateReadyRefusesMissingRecord(t *testing.T) {
	home := t.TempDir() // no router/tier1_eval.json written
	rec, recErr := sidecar.ReadEvalRecord(home)
	if recErr == nil {
		t.Fatal("ReadEvalRecord over an empty WATER_HOME should error")
	}
	if !errors.Is(recErr, os.ErrNotExist) {
		t.Fatalf("ReadEvalRecord error = %v, want it to wrap os.ErrNotExist", recErr)
	}
	ok, reason := tier1GateReady(rec, recErr, "some-registry-hash")
	if ok {
		t.Fatal("tier1GateReady should refuse when the record is missing")
	}
	if reason != "eval_missing" {
		t.Errorf("reason = %q, want %q", reason, "eval_missing")
	}
}

// TestTier1GateReadyRefusesStaleRecord: a record that exists but names a
// different intent registry (e.g. the twin's intent files changed since the
// eval ran) must also refuse to start Tier 1, distinctly from a missing
// record.
func TestTier1GateReadyRefusesStaleRecord(t *testing.T) {
	home := t.TempDir()
	rec := sidecar.EvalRecord{
		ModelSHA256: sidecar.ModelSHA256, RegistryHash: "old-hash",
		N: 400, FARate: 0, Wilson95Upper: 0, WarmP95Ms: 100,
	}
	if err := sidecar.WriteEvalRecord(home, rec); err != nil {
		t.Fatal(err)
	}
	got, recErr := sidecar.ReadEvalRecord(home)
	if recErr != nil {
		t.Fatal(recErr)
	}
	ok, reason := tier1GateReady(got, recErr, "current-hash")
	if ok {
		t.Fatal("tier1GateReady should refuse a record for a different registry hash")
	}
	if reason != sidecar.ReasonEvalStale {
		t.Errorf("reason = %q, want %q", reason, sidecar.ReasonEvalStale)
	}
}

// TestTier1GateReadyAcceptsPassingRecord: a record that matches today's
// model/registry and clears every threshold is accepted.
func TestTier1GateReadyAcceptsPassingRecord(t *testing.T) {
	home := t.TempDir()
	rec := sidecar.EvalRecord{
		ModelSHA256: sidecar.ModelSHA256, RegistryHash: "current-hash",
		N: 400, FARate: 0, Wilson95Upper: 0.01, WarmP95Ms: 200,
	}
	if err := sidecar.WriteEvalRecord(home, rec); err != nil {
		t.Fatal(err)
	}
	got, recErr := sidecar.ReadEvalRecord(home)
	if recErr != nil {
		t.Fatal(recErr)
	}
	ok, reason := tier1GateReady(got, recErr, "current-hash")
	if !ok {
		t.Fatalf("tier1GateReady should accept a passing record, got reason %q", reason)
	}
}
