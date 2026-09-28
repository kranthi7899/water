package decisions

import (
	"context"
	"testing"

	"water"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
)

// financeFigure is a company_finance result as gsheets.Normalize shapes it.
func financeFigure(tab, rangeA1, valuesJSON string) *store.FinanceFigure {
	return &store.FinanceFigure{
		Meta:    store.Meta{Source: "company_finance", SourceID: tab + "!" + rangeA1, External: true},
		Tab:     tab,
		RangeA1: rangeA1,
		Values:  valuesJSON,
	}
}

// TestBudgetRequestRegistryFileLoads validates the shipped
// twins/ceo/decisions/budget_request.yaml against the real ceo manifest,
// and checks it now fetches company_finance instead of the old Drive-CSV
// compute path.
func TestBudgetRequestRegistryFileLoads(t *testing.T) {
	m, err := twins.Load(water.TwinsFS(), "ceo")
	if err != nil {
		t.Fatal(err)
	}
	r, err := LoadRegistry(water.TwinsFS(), m)
	if err != nil {
		t.Fatalf("the shipped budget_request type must load: %v", err)
	}
	bt, ok := r.Lookup("budget_request")
	if !ok {
		t.Fatal("budget_request not registered")
	}
	if bt.Title == "" || bt.Trigger == "" || bt.DefaultRule == "" || bt.SeverityWeight < 1 {
		t.Fatalf("budget_request header: %+v", bt)
	}
	if len(bt.Needs) != 4 {
		t.Fatalf("budget_request needs: %+v", bt.Needs)
	}
	var sawCash, sawBudget bool
	for _, n := range bt.Needs {
		switch n.Fetch {
		case "company_finance.cash_position":
			sawCash = true
			if n.Internal() {
				t.Fatal("cash_position is a real connector fetch, not internal://")
			}
		case "company_finance.budget_status":
			sawBudget = true
			if n.Args["application"] != "{keywords}" {
				t.Fatalf("budget_status args: %+v", n.Args)
			}
		}
	}
	if !sawCash {
		t.Fatal("budget_request must fetch company_finance.cash_position")
	}
	if !sawBudget {
		t.Fatal("budget_request must fetch company_finance.budget_status")
	}
	// Only gmail.send_message (level A): gmail.draft_message is level D and
	// a staged action must be level A (see the yaml's own note on this).
	if len(bt.StagedActions) != 1 || bt.StagedActions[0] != "gmail.send_message" {
		t.Fatalf("staged actions: %+v", bt.StagedActions)
	}
}

// TestBudgetRequestDemoRegistryFileLoadsWithoutCompanyFinance checks the
// demo twin's simplified copy: no company_finance need (the demo twin has
// no company_finance connector — see the yaml's own notes), and it must
// still load and register cleanly.
func TestBudgetRequestDemoRegistryFileLoadsWithoutCompanyFinance(t *testing.T) {
	m, err := twins.Load(water.TwinsFS(), "ceo-demo")
	if err != nil {
		t.Fatal(err)
	}
	r, err := LoadRegistry(water.TwinsFS(), m)
	if err != nil {
		t.Fatalf("the shipped demo budget_request type must load: %v", err)
	}
	bt, ok := r.Lookup("budget_request")
	if !ok {
		t.Fatal("budget_request not registered in the demo registry")
	}
	for _, n := range bt.Needs {
		if n.Fetch == "company_finance.cash_position" || n.Fetch == "company_finance.budget_status" {
			t.Fatalf("the demo twin's budget_request must not fetch company_finance (no such connector on that twin): %+v", n)
		}
	}
}

// TestBudgetRequestCardEndToEnd builds a full card for the shipped real
// type through a fake gate, exercising the company_finance needs: the
// happy path is ready, cites company_finance evidence by tab!range, and
// is untrusted (Gmail, Drive and the finance sheet all went in).
func TestBudgetRequestCardEndToEnd(t *testing.T) {
	m, err := twins.Load(water.TwinsFS(), "ceo")
	if err != nil {
		t.Fatal(err)
	}
	r, err := LoadRegistry(water.TwinsFS(), m)
	if err != nil {
		t.Fatal(err)
	}
	item := msg("m1", true, "dana@x.com", "Need $1,500 for a new laptop")

	cash := financeFigure("Cash & runway", "B19:B21", `{"cash_usd":250000,"burn_usd":40000,"runway_months":6.25}`)
	budget := financeFigure("Budget", "A5:I11", `{"application":"crawler","matched":true}`)
	g := &fakeGate{answers: map[string]func(gate.Call) (gate.Result, error){
		"gmail.list_messages":           found(msg("h1", true, "dana@x.com", "Earlier ask")),
		"company_finance.cash_position": found(cash),
		"company_finance.budget_status": found(budget),
		"gdrive.search_files":           found(&store.Document{Meta: store.Meta{Source: "gdrive", SourceID: "d2", External: true}, Title: "Prior request"}),
	}}
	c, err := (&Builder{Registry: r, Gate: g}).Build(context.Background(), item, Classification{TypeID: "budget_request"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Readiness != Ready {
		t.Fatalf("readiness: %s, gaps %v", c.Readiness, c.Gaps)
	}
	if !c.Untrusted {
		t.Fatal("a card built from company_finance/Gmail/Drive content must be untrusted")
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("card failed its own invariant: %v", err)
	}

	var sawCashEvidence bool
	wantSource := "company_finance:Cash & runway!B19:B21"
	for _, e := range c.Evidence {
		if e.Source == wantSource {
			sawCashEvidence = true
			if e.Text == "" {
				t.Fatal("cash_position evidence has an empty description")
			}
		}
	}
	if !sawCashEvidence {
		t.Fatalf("no evidence cited %q — got: %+v", wantSource, c.Evidence)
	}

	// A gate that can't reach company_finance at all must not fail the
	// whole build — it degrades to missing_info, per the ordinary
	// readiness rule, never an error or a guessed number.
	g2 := &fakeGate{answers: map[string]func(gate.Call) (gate.Result, error){
		"gmail.list_messages":           found(msg("h1", true, "dana@x.com", "Earlier ask")),
		"company_finance.cash_position": none,
		"company_finance.budget_status": none,
		"gdrive.search_files":           none,
	}}
	c2, err := (&Builder{Registry: r, Gate: g2}).Build(context.Background(), item, Classification{TypeID: "budget_request"})
	if err != nil {
		t.Fatalf("no finance data must not fail the build: %v", err)
	}
	if c2.Readiness != MissingInfo {
		t.Fatalf("no finance data must degrade to missing_info, got %s; gaps %v", c2.Readiness, c2.Gaps)
	}
}
