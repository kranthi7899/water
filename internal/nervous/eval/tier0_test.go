package eval

import (
	"context"
	"testing"
	"time"

	"water"
	"water/internal/nervous"
	"water/internal/nervous/intents"
	"water/internal/nervous/propose"
	"water/internal/nervous/reflex"
	"water/internal/nervous/slots"
	"water/internal/nervous/tmpl"
	"water/internal/twins"
)

// tier0OnlyTier adapts the real Tier 0 cascade (eligibility, then
// nervous.TryTier0 — no Tier 1, no main path) to the Tier interface Run
// needs, over the REAL embedded twins/ceo/intents/*.yaml registry loaded
// exactly the way `water status`/the daemon load it (twins.Load +
// intents.LoadRegistry against water.TwinsFS()), not a MapFS fixture.
type tier0OnlyTier struct {
	reg  *intents.Registry
	deps reflex.Deps
	ents slots.Entities
	now  func() Fixture
}

func (t tier0OnlyTier) Try(ctx context.Context, utterance string, pending int) (matched bool, intent string, slotsOut map[string]string, escalate bool, err error) {
	fx := t.now()
	u := tmpl.Normalize(utterance, liveWordSet(t.reg.Shared().SkipWords))
	if ok, _ := nervous.Eligible(u, t.reg.Shared()); !ok {
		return false, "", nil, true, nil
	}
	res, reason, tErr := nervous.TryTier0(ctx, t.reg, t.deps, u, pending, fx.Now, t.ents, nil)
	if tErr != nil {
		// A handler error (e.g. a genuine cache miss) is an escalation for
		// eval purposes, not a crash and not a false accept.
		return false, "", nil, true, nil
	}
	if res != nil {
		return true, res.Intent, nil, false, nil
	}
	_ = reason
	return false, "", nil, true, nil
}

// TestTier0Eval is Slice R's acceptance criterion 1 and 2
// (docs/slices/R.md): 0 false accepts out of the 276-or-more held-out
// cases in testdata/ceo_eval.yaml, and a hit rate at least 50% and at
// least the recorded legacy fastpath baseline (6.2%) plus 20 points —
// measured against the REAL Tier 0 over the REAL embedded CEO registry,
// not a fixture mini-registry. Referenced by docs/architecture.md and
// docs/slices/R.md's own acceptance-criteria list; written during Slice
// R's live Phase 4 verification pass after discovering no test under this
// name actually existed yet, despite both docs citing it.
func TestTier0Eval(t *testing.T) {
	m, err := twins.Load(water.TwinsFS(), "ceo")
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	reg, err := intents.LoadRegistry(water.TwinsFS(), m, intents.Functions{Read: reflex.Specs(), Write: propose.Specs()}, intents.LoadOptions{})
	if err != nil {
		t.Fatalf("load registry: %v", err)
	}

	st, cleanup, err := fixtureStore()
	if err != nil {
		t.Fatalf("fixture store: %v", err)
	}
	t.Cleanup(cleanup)

	fx := DefaultFixture()
	deps := reflex.Deps{
		Store:     reflex.NewStoreView(st),
		Approvals: fixturePendingLister{},
		Now:       func() time.Time { return fx.Now },
		Manifest:  m,
		Brief: func(context.Context, string) (string, bool, bool, error) {
			return "", false, false, nil
		},
	}

	tier := tier0OnlyTier{reg: reg, deps: deps, ents: fixtureEntities(fx), now: func() Fixture { return fx }}

	cases, err := LoadCEOEval()
	if err != nil {
		t.Fatalf("load ceo_eval.yaml: %v", err)
	}
	if len(cases) < 276 {
		t.Fatalf("ceo_eval.yaml has %d cases, want >= 276 (Design section 18)", len(cases))
	}

	report := Run(context.Background(), tier, fx, cases)
	t.Logf("Tier0 report: N=%d hit_rate=%.3f intent_acc=%.3f slot_acc=%.3f fa_rate=%.4f wilson95=%.4f escalation_rate=%.3f reasoning_answered=%d",
		report.N, report.HitRate, report.IntentAcc, report.SlotAcc, report.FalseAcceptRate, report.Wilson95Upper, report.EscalationRate, report.ReasoningAnswered)

	if len(report.FalseAccepts) != 0 {
		for _, c := range report.FalseAccepts {
			t.Logf("false accept: utterance=%q want_intent=%q class=%q", c.Utterance, c.Intent, c.Class)
		}
		t.Fatalf("false accepts = %d, want 0", len(report.FalseAccepts))
	}
	if report.ReasoningAnswered != 0 {
		t.Fatalf("reasoning_answered = %d, want 0", report.ReasoningAnswered)
	}
	// Legacy fastpath baseline (recorded R-7, docs/slices/R.md): 6.2% hit
	// rate. Acceptance criterion 2: at least baseline+20 points, and at
	// least 50% outright.
	const legacyBaselineHitRate = 0.062
	if report.HitRate < legacyBaselineHitRate+0.20 {
		t.Errorf("hit rate = %.3f, want >= legacy baseline (%.3f) + 0.20 = %.3f", report.HitRate, legacyBaselineHitRate, legacyBaselineHitRate+0.20)
	}
	if report.HitRate < 0.50 {
		t.Errorf("hit rate = %.3f, want >= 0.50", report.HitRate)
	}
	if report.IntentAcc != 1 {
		t.Errorf("intent_acc = %.3f, want 1.000 (follows from 0 false accepts)", report.IntentAcc)
	}
}
