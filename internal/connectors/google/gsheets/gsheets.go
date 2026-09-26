// Package gsheets is the read-only "company_finance" connector: named,
// per-range lookups against Renaissance_Finance, the company's real
// financial model (a native Google Sheet in Drive) — cash, burn, runway,
// budget vs. actual, spend by application, outstanding invoices, revenue
// by application, funding history. It reads through the Sheets API's
// spreadsheets.values.get directly, not Drive's files.export (which gdrive
// already uses for a whole-file CSV export): a specific named tab and A1
// range needs the Sheets API's own scope (gapi.ScopeSheetsReadonly),
// separate from Drive's, since Drive's export endpoint can only reach one
// tab per file and this connector needs several different ones.
//
// A note on column semantics: the multi-row lookups below (budget_status,
// spend_breakdown, outstanding_invoices) match rows by scanning every cell
// for the caller's argument (or, for invoices, for "open") rather than by
// a hardcoded column position — this codebase has not yet had live access
// to the real sheet to confirm exactly which column holds what, and a
// position-agnostic scan is correct regardless. Each result also carries
// the full raw row (by column letter) alongside any best-effort labels, so
// nothing is lost if a label guess turns out wrong; correct it once real
// output is visible after the owner re-consents (see docs/google-setup.md).
package gsheets

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"water/internal/connectors"
	"water/internal/connectors/google/gapi"
	"water/internal/gate/permit"
	"water/internal/store"
	"water/internal/twins"
)

// spreadsheetID is Renaissance_Finance — one real sheet, not a per-call
// argument, since this connector exists for exactly this file. A different
// finance-like sheet (e.g. company_customers) gets its own connector.
const spreadsheetID = "1Jkt1WYl29KfzJAQvucarYVCA_5F5ibgCiGTDJnJassQ"

// lookup names one named function's fixed tab and A1 range.
type lookup struct {
	tab     string
	rangeA1 string
}

var lookups = map[string]lookup{
	"cash_position":          {"Cash & runway", "B19:B21"},
	"budget_status":          {"Budget", "A5:I11"},
	"spend_breakdown":        {"Spend by Application", "A5:H11"},
	"outstanding_invoices":   {"Invoices", "A5:I13"},
	"revenue_by_application": {"Revenue", "A16:Q24"},
	"funding_history":        {"Funding & Cap Table", "A5:I6"},
}

// Sheets is the "company_finance" connector. opts is nil in production;
// tests set it to point at an httptest server.
type Sheets struct {
	opts *gapi.Options
}

// New builds the production connector.
func New() *Sheets { return &Sheets{} }

// NewWithOptions builds a connector against a test double.
func NewWithOptions(o *gapi.Options) *Sheets { return &Sheets{opts: o} }

func (*Sheets) Name() string { return "company_finance" }

func (*Sheets) Credential() (string, string) { return gapi.Service, gapi.DefaultAccount }

func (*Sheets) Functions() []connectors.Function {
	return []connectors.Function{
		{
			Name:        "cash_position",
			Description: "Current cash, monthly burn and runway, from the Cash & runway tab.",
			Level:       twins.R, Risk: connectors.RiskLow, External: true,
			Schema: connectors.Schema{},
		},
		{
			Name:        "budget_status",
			Description: "Budget vs. actual for one application/product, from the Budget tab.",
			Level:       twins.R, Risk: connectors.RiskLow, External: true,
			Schema: connectors.Schema{
				Properties: map[string]connectors.Property{"application": {Type: "string", Description: "application/product name, e.g. \"crawler\""}},
				Required:   []string{"application"},
			},
		},
		{
			Name:        "spend_breakdown",
			Description: "R&D/admin/marketing spend, revenue and profit for one application/product, from the Spend by Application tab.",
			Level:       twins.R, Risk: connectors.RiskLow, External: true,
			Schema: connectors.Schema{
				Properties: map[string]connectors.Property{"application": {Type: "string", Description: "application/product name, e.g. \"crawler\""}},
				Required:   []string{"application"},
			},
		},
		{
			Name:        "outstanding_invoices",
			Description: "Open invoices, from the Invoices tab.",
			Level:       twins.R, Risk: connectors.RiskLow, External: true,
			Schema: connectors.Schema{},
		},
		{
			Name:        "revenue_by_application",
			Description: "Monthly revenue per application/product, from the Revenue tab's \"by application\" block.",
			Level:       twins.R, Risk: connectors.RiskLow, External: true,
			Schema: connectors.Schema{},
		},
		{
			Name:        "funding_history",
			Description: "Funding rounds, investors and amounts, from the Funding & Cap Table tab.",
			Level:       twins.R, Risk: connectors.RiskLow, External: true,
			Schema: connectors.Schema{},
		},
	}
}

func (c *Sheets) Invoke(ctx context.Context, p permit.Permit) (json.RawMessage, error) {
	v, err := p.Open()
	if err != nil {
		return nil, err
	}
	lk, ok := lookups[v.Function]
	if !ok {
		return nil, fmt.Errorf("company_finance: unknown function %q", v.Function)
	}
	cl, err := gapi.FromSecret(v.Credential, c.opts)
	if err != nil {
		return nil, err
	}
	grid, err := getRange(ctx, cl, lk.tab, lk.rangeA1)
	if err != nil {
		return nil, err
	}

	switch v.Function {
	case "cash_position":
		return cashPosition(lk, grid)
	case "budget_status":
		return matchApplication(lk, grid, gapi.ArgString(v.Args, "application"))
	case "spend_breakdown":
		return matchApplication(lk, grid, gapi.ArgString(v.Args, "application"))
	case "outstanding_invoices":
		return openInvoices(lk, grid)
	case "revenue_by_application", "funding_history":
		return rawGrid(lk, grid)
	}
	return nil, fmt.Errorf("company_finance: unknown function %q", v.Function)
}

// sheetsValuesResponse is the Sheets API's values.get response shape.
type sheetsValuesResponse struct {
	Range  string  `json:"range"`
	Values [][]any `json:"values"`
}

// getRange fetches one tab!range as a raw grid, unformatted (numbers decode
// as JSON numbers, not "$1,234"-style display strings).
func getRange(ctx context.Context, cl *gapi.Client, tab, rangeA1 string) ([][]any, error) {
	a1 := tab + "!" + rangeA1
	var resp sheetsValuesResponse
	q := url.Values{"valueRenderOption": {"UNFORMATTED_VALUE"}, "dateTimeRenderOption": {"FORMATTED_STRING"}}
	if err := cl.GetJSON(ctx, gapi.SheetsBase+"/"+url.PathEscape(spreadsheetID)+"/values/"+url.PathEscape(a1), q, &resp); err != nil {
		return nil, err
	}
	return resp.Values, nil
}

// columnLetters returns "A", "B", "C", ... for n columns.
func columnLetters(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = string(rune('A' + i))
	}
	return out
}

func rowContainsCI(row []any, needle string) bool {
	needle = strings.ToLower(strings.TrimSpace(needle))
	if needle == "" {
		return false
	}
	for _, cell := range row {
		if s, ok := cell.(string); ok && strings.Contains(strings.ToLower(s), needle) {
			return true
		}
	}
	return false
}

func cellFloat(row []any, i int) (float64, bool) {
	if i < 0 || i >= len(row) {
		return 0, false
	}
	switch v := row[i].(type) {
	case float64:
		return v, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil
	}
	return 0, false
}

type valuesOutput struct {
	Tab     string         `json:"tab"`
	Range   string         `json:"range"`
	ReadAt  string         `json:"read_at"`
	Values  map[string]any `json:"values"`
	Columns []string       `json:"columns,omitempty"`
	Row     []any          `json:"row,omitempty"`
	Rows    [][]any        `json:"rows,omitempty"`
	Matched *bool          `json:"matched,omitempty"`
}

func cashPosition(lk lookup, grid [][]any) (json.RawMessage, error) {
	get := func(i int) (float64, bool) {
		if i >= len(grid) || len(grid[i]) == 0 {
			return 0, false
		}
		return cellFloat(grid[i], 0)
	}
	cash, _ := get(0)
	burn, _ := get(1)
	runway, _ := get(2)
	out := valuesOutput{
		Tab: lk.tab, Range: lk.rangeA1, ReadAt: time.Now().UTC().Format(time.RFC3339),
		Values: map[string]any{"cash_usd": cash, "burn_usd": burn, "runway_months": runway},
	}
	return json.Marshal(out)
}

// matchApplication finds the first row whose cells mention application
// (case-insensitive substring), scanning cells positionally rather than
// trusting a guessed column index — see the package doc comment.
func matchApplication(lk lookup, grid [][]any, application string) (json.RawMessage, error) {
	out := valuesOutput{Tab: lk.tab, Range: lk.rangeA1, ReadAt: time.Now().UTC().Format(time.RFC3339)}
	for _, row := range grid {
		if rowContainsCI(row, application) {
			t := true
			out.Matched = &t
			out.Row = row
			out.Columns = columnLetters(len(row))
			out.Values = map[string]any{"application": application}
			return json.Marshal(out)
		}
	}
	f := false
	out.Matched = &f
	out.Values = map[string]any{"application": application}
	return json.Marshal(out)
}

// openInvoices returns every row whose cells mention "open" — a
// position-agnostic stand-in for filtering on a Status column, per the
// package doc comment.
func openInvoices(lk lookup, grid [][]any) (json.RawMessage, error) {
	var rows [][]any
	for _, row := range grid {
		if rowContainsCI(row, "open") {
			rows = append(rows, row)
		}
	}
	if rows == nil {
		rows = [][]any{}
	}
	out := valuesOutput{
		Tab: lk.tab, Range: lk.rangeA1, ReadAt: time.Now().UTC().Format(time.RFC3339),
		Rows: rows,
	}
	if len(grid) > 0 {
		out.Columns = columnLetters(len(grid[0]))
	}
	return json.Marshal(out)
}

func rawGrid(lk lookup, grid [][]any) (json.RawMessage, error) {
	if grid == nil {
		grid = [][]any{}
	}
	out := valuesOutput{Tab: lk.tab, Range: lk.rangeA1, ReadAt: time.Now().UTC().Format(time.RFC3339), Rows: grid}
	if len(grid) > 0 {
		out.Columns = columnLetters(len(grid[0]))
	}
	return json.Marshal(out)
}

func (c *Sheets) Normalize(function string, raw json.RawMessage) ([]store.Record, error) {
	lk, ok := lookups[function]
	if !ok {
		return nil, nil
	}
	var out valuesOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	valuesJSON, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	sourceID := lk.tab + "!" + lk.rangeA1
	// budget_status/spend_breakdown share one fixed range across every
	// application queried against it — without this, a second application
	// would Upsert over the same (source, source_id) and silently replace
	// the first one's stored record, even though the evidence-citation
	// path itself (within one decision Build call) is unaffected, since
	// that reads straight from this call's own Result.Records, never back
	// out of the store.
	if application, ok := out.Values["application"].(string); ok && application != "" {
		sourceID += "#" + application
	}
	return []store.Record{&store.FinanceFigure{
		Meta:    store.Meta{Source: c.Name(), SourceID: sourceID, External: true},
		Tab:     lk.tab,
		RangeA1: lk.rangeA1,
		Values:  string(valuesJSON),
	}}, nil
}
