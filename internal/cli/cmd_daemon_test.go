package cli

import (
	"reflect"
	"testing"
	"time"

	"water/internal/config"
	"water/internal/nervous"
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
