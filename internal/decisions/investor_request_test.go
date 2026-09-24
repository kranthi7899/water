package decisions

import (
	"context"
	"testing"
	"time"

	"water"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
)

// hubspotDeal is a hubspot.list_deals result as fake.HubSpot.Normalize
// shapes it: Account carries the pipeline stage (see hubspot.go's Normalize
// comment), PostedAt the close date.
func hubspotDeal(id, company, description, stage string, amountMinor int64, closeIn time.Duration) *store.Transaction {
	return &store.Transaction{
		Meta:         store.Meta{Source: "hubspot", SourceID: id, External: true},
		Account:      stage,
		AmountMinor:  amountMinor,
		Currency:     "USD",
		Counterparty: company,
		Description:  description,
		PostedAt:     time.Now().Add(closeIn).UTC(),
	}
}

func TestComputeDealHealthComputesAmountStageAndDaysToClose(t *testing.T) {
	deal := hubspotDeal("deal-1", "Meridian Ventures", "Meridian Ventures — Series B follow-on discussion", "Follow-up needed", 3_000_000_00, 21*24*time.Hour)
	res, err := computeDealHealth(context.Background(), ComputeInput{
		Resolved: map[string]NeedResult{investorDealNeed: {Records: []store.Record{deal}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.External {
		t.Fatal("a figure computed from a HubSpot deal must be marked External")
	}
	if len(res.Evidence) != 1 || res.Evidence[0].Source != computeDealHealthSource {
		t.Fatalf("evidence: %+v", res.Evidence)
	}
	if f := res.Figures["deal_amount_usd"]; f.Value != 3_000_000.0 || f.Source != computeDealHealthSource {
		t.Fatalf("deal_amount_usd: %+v", f)
	}
	if f := res.Figures["deal_stage"]; f.Value != "Follow-up needed" || f.Source != computeDealHealthSource {
		t.Fatalf("deal_stage: %+v", f)
	}
	if f := res.Figures["days_to_close"]; f.Source != computeDealHealthSource {
		t.Fatalf("days_to_close: %+v", f)
	} else if d, ok := f.Value.(int); !ok || d < 20 || d > 22 {
		t.Fatalf("days_to_close = %v, want ~21", f.Value)
	}
}

func TestComputeDealHealthEmptyInputsAreMissingInfoNotACrash(t *testing.T) {
	cases := map[string]ComputeInput{
		"no deal need resolved yet": {Resolved: map[string]NeedResult{}},
		"need present, no records":  {Resolved: map[string]NeedResult{investorDealNeed: {}}},
		"record is not a transaction": {Resolved: map[string]NeedResult{
			investorDealNeed: {Records: []store.Record{msg("m1", true, "a@x.com", "not a deal")}},
		}},
		"transaction has no close date": {Resolved: map[string]NeedResult{
			investorDealNeed: {Records: []store.Record{
				&store.Transaction{Meta: store.Meta{Source: "hubspot", SourceID: "d1"}, Counterparty: "X"},
			}},
		}},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := computeDealHealth(context.Background(), in)
			if err != nil {
				t.Fatalf("must never error, only return empty: %v", err)
			}
			if len(res.Figures) != 0 || len(res.Evidence) != 0 || res.External {
				t.Fatalf("empty/malformed input must yield an empty result: %+v", res)
			}
		})
	}
}

// TestInvestorRequestDemoRegistryFileLoads validates the shipped
// twins/ceo-demo/decisions/investor_request.yaml against the real ceo-demo
// manifest through the real embedded filesystem: the HubSpot-backed variant
// this slice adds.
func TestInvestorRequestDemoRegistryFileLoads(t *testing.T) {
	m, err := twins.Load(water.TwinsFS(), "ceo-demo")
	if err != nil {
		t.Fatal(err)
	}
	r, err := LoadRegistry(water.TwinsFS(), m)
	if err != nil {
		t.Fatalf("the shipped demo investor_request type must load: %v", err)
	}
	it, ok := r.Lookup("investor_request")
	if !ok {
		t.Fatal("investor_request not registered in the demo registry")
	}
	var sawHubSpot, sawCompute bool
	for _, n := range it.Needs {
		if n.Fetch == "hubspot.list_deals" {
			sawHubSpot = true
		}
		if n.Fetch == "internal://investor_request.compute_deal_health" {
			sawCompute = true
			if !n.Internal() {
				t.Fatal("compute_deal_health need must be internal")
			}
		}
	}
	if !sawHubSpot {
		t.Fatal("demo investor_request must fetch hubspot.list_deals")
	}
	if !sawCompute {
		t.Fatal("demo investor_request must use internal://investor_request.compute_deal_health")
	}

	// The real (non-demo) manifest must never be asked to validate this
	// need: hubspot is not one of its connectors, by design.
	real, err := twins.Load(water.TwinsFS(), "ceo")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := real.Function("hubspot.list_deals"); ok {
		t.Fatal("hubspot.list_deals must not exist in the real ceo manifest")
	}
}

// TestInvestorRequestDemoCardEndToEnd builds a full card for the shipped
// demo type through a fake gate: the happy path is ready with a sourced
// deal figure and untrusted, and a gate that finds no matching deal
// degrades to missing_info through the ordinary readiness path.
func TestInvestorRequestDemoCardEndToEnd(t *testing.T) {
	m, err := twins.Load(water.TwinsFS(), "ceo-demo")
	if err != nil {
		t.Fatal(err)
	}
	r, err := LoadRegistry(water.TwinsFS(), m)
	if err != nil {
		t.Fatal(err)
	}
	item := msg("m1", true, "elena.cross@meridianvc.example", "Following up on the Meridian Series B")

	deal := hubspotDeal("deal-1004", "Meridian Ventures", "Meridian Ventures — Series B follow-on discussion", "Follow-up needed", 300_000_000, 21*24*time.Hour)
	g := &fakeGate{answers: map[string]func(gate.Call) (gate.Result, error){
		"gmail.list_messages": found(msg("h1", true, "elena.cross@meridianvc.example", "Earlier note")),
		"gdrive.search_files": found(&store.Document{Meta: store.Meta{Source: "gdrive", SourceID: "d1", External: true}, Title: "Meridian data room"}),
		"hubspot.list_deals":  found(deal),
	}}
	c, err := (&Builder{Registry: r, Gate: g}).Build(context.Background(), item, Classification{TypeID: "investor_request"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Readiness != Ready {
		t.Fatalf("readiness: %s, gaps %v", c.Readiness, c.Gaps)
	}
	if c.Defaults["deal_amount_usd"] != 3_000_000.0 || c.DefaultSources["deal_amount_usd"] != computeDealHealthSource {
		t.Fatalf("deal_amount_usd: %+v %+v", c.Defaults, c.DefaultSources)
	}
	if c.Defaults["deal_stage"] != "Follow-up needed" {
		t.Fatalf("deal_stage: %+v", c.Defaults)
	}
	if !c.Untrusted {
		t.Fatal("a card built from HubSpot/Gmail/Drive content must be untrusted")
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("card failed its own invariant: %v", err)
	}

	g2 := &fakeGate{answers: map[string]func(gate.Call) (gate.Result, error){
		"gmail.list_messages": found(msg("h1", true, "elena.cross@meridianvc.example", "Earlier note")),
		"gdrive.search_files": none,
		"hubspot.list_deals":  none,
	}}
	c2, err := (&Builder{Registry: r, Gate: g2}).Build(context.Background(), item, Classification{TypeID: "investor_request"})
	if err != nil {
		t.Fatalf("no matching deal must not fail the build: %v", err)
	}
	if c2.Readiness != MissingInfo {
		t.Fatalf("no matching deal must degrade to missing_info, got %s; gaps %v", c2.Readiness, c2.Gaps)
	}
	if _, ok := c2.Defaults["deal_amount_usd"]; ok {
		t.Fatal("no deal figure should reach the card when nothing was found")
	}
}

// TestInvestorRequestCardWithoutSourcingIsRejected is the property-style
// check docs/slices/C.md's "every figure has a source" test asks for,
// specific to this type: a hand-built card whose figure names no source
// fails Validate, the same invariant budget_request's own figures satisfy.
func TestInvestorRequestCardWithoutSourcingIsRejected(t *testing.T) {
	c := &Card{
		ID:             "card-x",
		TypeID:         "investor_request",
		Defaults:       map[string]any{"deal_amount_usd": 3_000_000},
		DefaultSources: map[string]string{}, // no source recorded
		Parameters:     map[string]any{"deal_amount_usd": 3_000_000},
	}
	if err := c.Validate(); err == nil {
		t.Fatal("a sourceless figure must be rejected by Card.Validate")
	}
}
