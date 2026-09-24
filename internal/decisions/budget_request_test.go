package decisions

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"water"
	"water/internal/connectors/google/gdrive"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
)

// budgetSheet is a Drive read_file result as gdrive.Normalize shapes it:
// the full text in Content, the first 280 runes in Excerpt.
func budgetSheet(id, content string) *store.Document {
	ex := []rune(content)
	if len(ex) > 280 {
		ex = ex[:280]
	}
	return &store.Document{Meta: store.Meta{Source: "gdrive", SourceID: id, External: true}, Title: "Runway", Excerpt: string(ex), Content: content}
}

func TestComputeRunwayParsesTheLatestRow(t *testing.T) {
	item := msg("m1", true, "dana@x.com", "Budget ask")
	sheet := budgetSheet("sheet1", "month,cash,burn\nJan,120000,20000\nFeb,100000,25000\n")
	res, err := computeRunway(context.Background(), ComputeInput{
		Item:     item,
		Resolved: map[string]NeedResult{budgetSpreadsheetNeed: {Records: []store.Record{sheet}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.External {
		t.Fatal("a figure computed from a Drive sheet must be marked External")
	}
	if len(res.Evidence) != 1 || res.Evidence[0].Source != computeRunwaySource {
		t.Fatalf("evidence: %+v", res.Evidence)
	}
	want := map[string]float64{"cash_on_hand": 100000, "monthly_burn": 25000, "runway_months": 4}
	for k, wantV := range want {
		f, ok := res.Figures[k]
		if !ok || f.Value != wantV || f.Source != computeRunwaySource {
			t.Fatalf("figure %s: %+v, want %v [%s]", k, f, wantV, computeRunwaySource)
		}
	}
}

func TestComputeRunwayAcceptsHeaderVariantsAndTabsAndMoney(t *testing.T) {
	item := msg("m1", true, "dana@x.com", "Budget ask")
	sheet := budgetSheet("sheet1", "Month\tCash On Hand\tMonthly Burn\nJan\t$120,000\t$20,000.50\n")
	res, err := computeRunway(context.Background(), ComputeInput{
		Item:     item,
		Resolved: map[string]NeedResult{budgetSpreadsheetNeed: {Records: []store.Record{sheet}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Figures["cash_on_hand"].Value != 120000.0 || res.Figures["monthly_burn"].Value != 20000.5 {
		t.Fatalf("figures: %+v", res.Figures)
	}
}

func TestComputeRunwayMalformedInputsAreMissingInfoNotACrash(t *testing.T) {
	item := msg("m1", true, "dana@x.com", "Budget ask")
	cases := map[string]ComputeInput{
		"no spreadsheet need resolved yet": {Item: item, Resolved: map[string]NeedResult{}},
		"need present, no records":         {Item: item, Resolved: map[string]NeedResult{budgetSpreadsheetNeed: {}}},
		"record is not a document": {Item: item, Resolved: map[string]NeedResult{
			budgetSpreadsheetNeed: {Records: []store.Record{msg("m9", true, "a@x", "not a sheet")}},
		}},
		"empty excerpt": {Item: item, Resolved: map[string]NeedResult{
			budgetSpreadsheetNeed: {Records: []store.Record{budgetSheet("s1", "")}},
		}},
		"no header row at all": {Item: item, Resolved: map[string]NeedResult{
			budgetSpreadsheetNeed: {Records: []store.Record{budgetSheet("s1", "just one line, no comma or newline")}},
		}},
		"header without cash or burn columns": {Item: item, Resolved: map[string]NeedResult{
			budgetSpreadsheetNeed: {Records: []store.Record{budgetSheet("s1", "month,notes\nJan,ok\n")}},
		}},
		"non-numeric rows": {Item: item, Resolved: map[string]NeedResult{
			budgetSpreadsheetNeed: {Records: []store.Record{budgetSheet("s1", "month,cash,burn\nJan,tbd,tbd\n")}},
		}},
		"zero burn is not a runway": {Item: item, Resolved: map[string]NeedResult{
			budgetSpreadsheetNeed: {Records: []store.Record{budgetSheet("s1", "month,cash,burn\nJan,50000,0\n")}},
		}},
		"malformed csv (unterminated quote)": {Item: item, Resolved: map[string]NeedResult{
			budgetSpreadsheetNeed: {Records: []store.Record{budgetSheet("s1", "month,cash,burn\n\"Jan,1000,200\n")}},
		}},
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			res, err := computeRunway(context.Background(), in)
			if err != nil {
				t.Fatalf("must never error, only return empty: %v", err)
			}
			if len(res.Figures) != 0 || len(res.Evidence) != 0 || res.External {
				t.Fatalf("malformed input must yield an empty result: %+v", res)
			}
		})
	}
}

// TestBudgetRequestRegistryFileLoads validates the shipped
// twins/ceo/decisions/budget_request.yaml against the real ceo manifest
// through the real embedded filesystem, and checks its shape.
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
	var sawCompute bool
	for _, n := range bt.Needs {
		if n.Name == budgetSpreadsheetNeed {
			if n.Fetch != "gdrive.read_file" || n.Args["id"] == "" {
				t.Fatalf("budget_spreadsheet need: %+v", n)
			}
		}
		if n.Fetch == "internal://budget_request.compute_runway" {
			sawCompute = true
			if !n.Internal() {
				t.Fatal("compute_runway need must be internal")
			}
		}
	}
	if !sawCompute {
		t.Fatal("budget_request must use internal://budget_request.compute_runway")
	}
	if len(bt.StagedActions) != 2 {
		t.Fatalf("staged actions: %+v", bt.StagedActions)
	}
}

// TestBudgetRequestCardEndToEnd builds a full card for the shipped type
// through a fake gate, exercising the compute function inside a real Build
// call: the happy path is ready with a sourced runway figure and untrusted
// (a Drive sheet and external mail both went in), and a malformed sheet
// degrades to missing_info through the ordinary readiness path, not a panic
// or an error return.
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

	good := budgetSheet("sheet1", "month,cash,burn\nJan,120000,20000\nFeb,100000,25000\n")
	g := &fakeGate{answers: map[string]func(gate.Call) (gate.Result, error){
		"gmail.list_messages": found(msg("h1", true, "dana@x.com", "Earlier ask")),
		"gdrive.read_file":    found(good),
		"gdrive.search_files": found(&store.Document{Meta: store.Meta{Source: "gdrive", SourceID: "d2", External: true}, Title: "Prior request"}),
	}}
	c, err := (&Builder{Registry: r, Gate: g}).Build(context.Background(), item, Classification{TypeID: "budget_request"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Readiness != Ready {
		t.Fatalf("readiness: %s, gaps %v", c.Readiness, c.Gaps)
	}
	if c.Defaults["runway_months"] != 4.0 || c.DefaultSources["runway_months"] != computeRunwaySource {
		t.Fatalf("runway figure: %+v %+v", c.Defaults, c.DefaultSources)
	}
	if !c.Untrusted {
		t.Fatal("a card built from Drive/Gmail content must be untrusted")
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("card failed its own invariant: %v", err)
	}

	badSheet := budgetSheet("sheet1", "not a budget sheet at all")
	g2 := &fakeGate{answers: map[string]func(gate.Call) (gate.Result, error){
		"gmail.list_messages": found(msg("h1", true, "dana@x.com", "Earlier ask")),
		"gdrive.read_file":    found(badSheet),
		"gdrive.search_files": none,
	}}
	c2, err := (&Builder{Registry: r, Gate: g2}).Build(context.Background(), item, Classification{TypeID: "budget_request"})
	if err != nil {
		t.Fatalf("a malformed spreadsheet must not fail the build: %v", err)
	}
	if c2.Readiness != MissingInfo {
		t.Fatalf("malformed spreadsheet must degrade to missing_info, got %s; gaps %v", c2.Readiness, c2.Gaps)
	}
	if _, ok := c2.Defaults["runway_months"]; ok {
		t.Fatal("no runway figure should reach the card from an unreadable spreadsheet")
	}
}

// twelveMonthCSV is a realistic runway export whose rows run past the
// 280-rune Drive excerpt, with a burn whose digits a cut row would truncate.
func twelveMonthCSV() string {
	var b strings.Builder
	b.WriteString("month,cash_on_hand,monthly_burn\n")
	for i := 1; i <= 12; i++ {
		fmt.Fprintf(&b, "2026-%02d,%d,45000\n", i, 900000-45000*i)
	}
	return b.String()
}

func gdriveReadFile(t *testing.T, content string, truncated bool) store.Record {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"id": "sheet1", "name": "Runway", "mimeType": "application/vnd.google-apps.spreadsheet", "content": content, "truncated": truncated})
	if err != nil {
		t.Fatal(err)
	}
	recs, err := (&gdrive.Drive{}).Normalize("read_file", raw)
	if err != nil || len(recs) != 1 {
		t.Fatalf("normalize: %v %+v", err, recs)
	}
	return recs[0]
}

// TestComputeRunwayReadsTheWholeSheetNotTheExcerpt runs a real 12-month
// export through the real gdrive normalizer: the runway must come from the
// last full row, never a row cut in half at the 280-rune excerpt boundary.
func TestComputeRunwayReadsTheWholeSheetNotTheExcerpt(t *testing.T) {
	sheet := gdriveReadFile(t, twelveMonthCSV(), false)
	res, err := computeRunway(context.Background(), ComputeInput{Resolved: map[string]NeedResult{budgetSpreadsheetNeed: {Records: []store.Record{sheet}}}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Figures["monthly_burn"].Value != 45000.0 || res.Figures["cash_on_hand"].Value != 360000.0 || res.Figures["runway_months"].Value != 8.0 {
		t.Fatalf("figures: %+v", res.Figures)
	}

	cut := gdriveReadFile(t, twelveMonthCSV()[:250], true)
	res, err = computeRunway(context.Background(), ComputeInput{Resolved: map[string]NeedResult{budgetSpreadsheetNeed: {Records: []store.Record{cut}}}})
	if err != nil || len(res.Figures) != 0 {
		t.Fatalf("a truncated export has lost its latest months and must be missing_info: %+v %v", res, err)
	}
}

func TestParseBudgetCSVRejectsNonFiniteAndNegativeAmounts(t *testing.T) {
	for name, s := range map[string]string{
		"nan":           "cash,burn\nNaN,NaN\n",
		"inf burn":      "cash,burn\n100000,Inf\n",
		"infinity burn": "cash,burn\n100000,infinity\n",
		"-inf cash":     "cash,burn\n-Inf,1000\n",
		"inf cash":      "cash,burn\n+Inf,1000\n",
		"negative cash": "cash,burn\n$-5000,1000\n",
		"hex":           "cash,burn\n0x10,0x10\n",
		"negative burn": "cash,burn\n5000,-1000\n",
	} {
		t.Run(name, func(t *testing.T) {
			if c, b, n, ok := parseBudgetCSV(s); ok {
				t.Fatalf("accepted %q: cash %v burn %v rows %d", s, c, b, n)
			}
		})
	}
}
