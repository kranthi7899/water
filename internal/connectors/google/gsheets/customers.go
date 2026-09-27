// customers.go: the read-only "company_customers" connector
// (docs/slices/UI.md U3-A), a named, explicit exception to "no new
// connectors" the owner approved specifically for this: a second Google
// Sheet, Renaissance_Customers, read the same way company_finance reads
// Renaissance_Finance in gsheets.go — same package, same shared
// water.google/ceo credential (gapi.Service/gapi.DefaultAccount), same
// values.get-by-tab/range pattern, R level only, rate-capped in
// twins/ceo/twin.yaml exactly like every other read-only function here.
//
// The owner has not uploaded the spreadsheet yet (Renaissance_Customers.xlsx
// as a native Google Sheet in the Water Google account). Until they do,
// customersSpreadsheetID stays the CONFIGURE_ME placeholder below, and
// Invoke refuses with ErrCustomersNotConfigured before ever making a Sheets
// API call — the same "no repo configured" precedent
// internal/connectors/github's own Invoke uses for githubRepo == "", and
// the same CONFIGURE_ME_... placeholder shape budget_request.yaml's now-
// retired internal://budget_request.compute_runway once used
// (CONFIGURE_ME_drive_file_id_of_the_runway_spreadsheet). This is expected,
// correct behavior today, not a bug: internal/dashboards/compute.go maps
// this error (and a missing shared Google credential) to the Clients
// dashboard's "not_connected" tile state.
//
// Row schema (Accounts tab, once the owner's real sheet exists): this
// package has no live access to the real spreadsheet, so the schema below
// is this task's own minimal, documented guess, not a confirmed layout —
// correct it once the sheet is real (see docs/google-setup.md). One row
// per customer account:
//
//	A  account name (string)
//	B  health ("healthy" | "at_risk" | "critical", case-insensitive; any
//	   other non-empty value is still counted as "not healthy" for
//	   accounts_at_risk, and shown as its own group in accounts_by_health)
//	C  open tickets (integer)
//	D  NPS score (integer, blank if no survey response yet)
//	E  last contact date ("YYYY-MM-DD")
//	F  notes (free text, not read by any compute function)
package gsheets

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"water/internal/connectors"
	"water/internal/connectors/google/gapi"
	"water/internal/gate/permit"
	"water/internal/store"
	"water/internal/twins"
)

// customersSpreadsheetID is a placeholder until the owner uploads
// Renaissance_Customers.xlsx as a native Google Sheet and this constant is
// hand-edited to its real id — exactly how gsheets.go's own spreadsheetID
// const works, just not filled in yet. It is never a real Google
// spreadsheet id (Google's ids don't start with "CONFIGURE_ME_"), so
// Invoke below can recognize it and refuse cleanly instead of making a
// doomed API call against it.
const customersSpreadsheetID = "CONFIGURE_ME_company_customers_spreadsheet_id"

// customersConfigurePrefix is the sentinel prefix Invoke checks for. A
// test overrides customersSpreadsheetID (via NewCustomersWithOptions) to
// something else entirely, so this checks the prefix, not equality with
// the specific placeholder string above.
const customersConfigurePrefix = "CONFIGURE_ME_"

// customersLookup is the one named function's fixed tab and A1 range, the
// same shape gsheets.go's own `lookup` type has (kept separate so this
// file has no dependency on gsheets.go's lookups map, which is keyed by
// company_finance's own function names).
var customersLookup = lookup{tab: "Accounts", rangeA1: "A5:F55"}

// ErrCustomersNotConfigured is returned by Customers.Invoke while
// customersSpreadsheetID is still the CONFIGURE_ME placeholder — i.e., the
// owner has not yet uploaded Renaissance_Customers.xlsx and named its real
// spreadsheet id here. internal/dashboards/compute.go matches this with
// errors.Is to produce the "not_connected" tile state, the same state a
// missing shared Google credential produces via the gate's own "credential
// ... is unavailable" denial: from the CEO's point of view both mean
// exactly the same thing, "there is nothing to read yet."
var ErrCustomersNotConfigured = errors.New("company_customers: spreadsheet not configured yet; see docs/slices/UI.md U3-A")

// Customers is the "company_customers" connector. opts is nil in
// production and set in tests to point at an httptest server.
type Customers struct {
	spreadsheetID string
	opts          *gapi.Options
}

// NewCustomers builds the production connector, always against the
// CONFIGURE_ME placeholder until this file is hand-edited with the
// owner's real spreadsheet id.
func NewCustomers() *Customers { return &Customers{spreadsheetID: customersSpreadsheetID} }

// NewCustomersWithOptions builds a connector against a test double, and
// optionally a non-placeholder spreadsheet id so a test can exercise the
// "configured" path without waiting for the owner's real upload (an empty
// id keeps the production placeholder, so a test can also exercise
// ErrCustomersNotConfigured). This differs from gsheets.NewWithOptions,
// which never needs to override spreadsheetID, because company_customers'
// whole point right now is that its id isn't real yet.
func NewCustomersWithOptions(spreadsheetID string, o *gapi.Options) *Customers {
	if spreadsheetID == "" {
		spreadsheetID = customersSpreadsheetID
	}
	return &Customers{spreadsheetID: spreadsheetID, opts: o}
}

func (*Customers) Name() string { return "company_customers" }

func (*Customers) Credential() (string, string) { return gapi.Service, gapi.DefaultAccount }

func (*Customers) Functions() []connectors.Function {
	return []connectors.Function{
		{
			Name:        "accounts",
			Description: "Every customer account: health, open tickets, NPS and last contact date, from the Accounts tab.",
			Activity:    "Reading customer accounts",
			Level:       twins.R, Risk: connectors.RiskLow, External: true,
			Schema: connectors.Schema{},
		},
	}
}

func (c *Customers) Invoke(ctx context.Context, p permit.Permit) (json.RawMessage, error) {
	v, err := p.Open()
	if err != nil {
		return nil, err
	}
	if v.Function != "accounts" {
		return nil, fmt.Errorf("company_customers: unknown function %q", v.Function)
	}
	if strings.HasPrefix(c.spreadsheetID, customersConfigurePrefix) {
		return nil, ErrCustomersNotConfigured
	}
	cl, err := gapi.FromSecret(v.Credential, c.opts)
	if err != nil {
		return nil, err
	}
	grid, err := getRange(ctx, cl, c.spreadsheetID, customersLookup.tab, customersLookup.rangeA1)
	if err != nil {
		return nil, err
	}
	return rawGrid(customersLookup, grid)
}

// Normalize stores the whole accounts grid as one FinanceFigure-shaped
// record, the same store.Record type gsheets.go's own Normalize uses for
// every other multi-row read (revenue_by_application, funding_history):
// there is no dedicated customers record type, and the dashboard compute
// layer (internal/dashboards/compute.go) reads straight from the gate's
// live Result, not back out of the store.
func (c *Customers) Normalize(function string, raw json.RawMessage) ([]store.Record, error) {
	if function != "accounts" {
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
	return []store.Record{&store.FinanceFigure{
		Meta:    store.Meta{Source: c.Name(), SourceID: customersLookup.tab + "!" + customersLookup.rangeA1, External: true},
		Tab:     customersLookup.tab,
		RangeA1: customersLookup.rangeA1,
		Values:  string(valuesJSON),
	}}, nil
}
