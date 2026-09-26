package store

import (
	"context"
	"testing"
)

func TestFinanceFigureUpsertGetAndDecode(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	fig := &FinanceFigure{
		Meta: Meta{Source: "company_finance", SourceID: "Cash & runway!B19:B21", External: true},
		Tab:  "Cash & runway", RangeA1: "B19:B21",
		Values: `{"cash_usd":250000,"burn_usd":40000,"runway_months":6.25}`,
	}
	if err := s.Upsert(ctx, fig); err != nil {
		t.Fatal(err)
	}
	got, err := Get[FinanceFigure](ctx, s, "company_finance", "Cash & runway!B19:B21")
	if err != nil {
		t.Fatal(err)
	}
	if got.Tab != "Cash & runway" || got.RangeA1 != "B19:B21" {
		t.Fatalf("got = %+v", got)
	}
	var vals struct {
		CashUSD      float64 `json:"cash_usd"`
		BurnUSD      float64 `json:"burn_usd"`
		RunwayMonths float64 `json:"runway_months"`
	}
	if err := got.Decode(&vals); err != nil {
		t.Fatal(err)
	}
	if vals.CashUSD != 250000 || vals.BurnUSD != 40000 || vals.RunwayMonths != 6.25 {
		t.Fatalf("decoded values = %+v", vals)
	}
}

func TestFinanceFigureDecodeMalformedJSONReturnsError(t *testing.T) {
	fig := FinanceFigure{Values: "not json"}
	var out map[string]any
	if err := fig.Decode(&out); err == nil {
		t.Fatal("Decode on malformed JSON = nil error, want an error (not a panic, not silently empty)")
	}
}

// TestFinanceFigureSourceIDDoublesAsCitation confirms the design choice
// documented on the type: SourceID is "<Tab>!<Range>", which is exactly
// what internal/decisions' Ref() (Source + ":" + SourceID) needs to build
// an evidence citation with no extra fields.
func TestFinanceFigureSourceIDDoublesAsCitation(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	fig := &FinanceFigure{
		Meta: Meta{Source: "company_finance", SourceID: "Budget!A5:I11", External: true},
		Tab:  "Budget", RangeA1: "A5:I11",
		Values: `{"application":"crawler","budget_usd":50000,"actual_usd":48000}`,
	}
	if err := s.Upsert(ctx, fig); err != nil {
		t.Fatal(err)
	}
	got, err := Get[FinanceFigure](ctx, s, "company_finance", "Budget!A5:I11")
	if err != nil {
		t.Fatal(err)
	}
	citation := got.Meta.Source + ":" + got.Meta.SourceID
	if citation != "company_finance:Budget!A5:I11" {
		t.Fatalf("citation = %q, want %q", citation, "company_finance:Budget!A5:I11")
	}
}
