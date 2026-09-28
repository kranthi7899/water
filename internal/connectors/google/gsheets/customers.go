// customers.go: the read-only "company_customers" connector
// (docs/slices/UI.md U3-A), a named, explicit exception to "no new
// connectors" the owner approved specifically for this: a second Google
// Sheet, Renaissance_Customers, read the same way company_finance reads
// Renaissance_Finance in gsheets.go — same package, same shared
// water.google/ceo credential (gapi.Service/gapi.DefaultAccount), same
// values.get-by-tab/range pattern, R level only, rate-capped in
// twins/ceo/twin.yaml exactly like every other read-only function here.
//
// The owner uploaded Renaissance_Customers.xlsx as a native Google Sheet,
// shared it Viewer with water.google/ceo, and customersSpreadsheetID below
// is now the real id (2026-09-27) — live read access was confirmed
// directly against the Sheets API with Water's own stored credential
// before this was filled in, both for reachability (Summary!A1:H12) and
// for the exact production range (Accounts!A6:Q60, 10 real rows, matching
// the source file exactly). If the spreadsheet id is ever reset to a
// CONFIGURE_ME_-prefixed placeholder (e.g. in a test), Invoke refuses with
// ErrCustomersNotConfigured before ever making a Sheets API call — the
// same "no repo configured" precedent internal/connectors/github's own
// Invoke uses for githubRepo == "", and the same CONFIGURE_ME_...
// placeholder shape budget_request.yaml's now-retired
// internal://budget_request.compute_runway once used
// (CONFIGURE_ME_drive_file_id_of_the_runway_spreadsheet).
// internal/dashboards/compute.go maps that error (and a missing shared
// Google credential) to the Clients dashboard's "not_connected" tile state.
//
// Row schema (Accounts tab), corrected against the owner's actual
// Renaissance_Customers.xlsx (2026-09-27) — the original schema above this
// comment was this task's own unconfirmed guess and was wrong. Header row
// is row 5 (rows 1-4 are a title/note/as-of-date block); data runs rows
// 6-15 today, hence customersLookup's "A6:Q60" (not "A5"). One row per
// customer, prospect or partner:
//
//	A  account id (string, e.g. "meridian-records") — not used for display
//	B  name (string) — the join key dashboards.go's accountColName reads
//	C  product
//	D  type (Customer | Pilot | Prospect | Design partner)
//	E  segment (free text)
//	F  owner (person)
//	G  MRR (number)
//	H  contract start (date)
//	I  renewal / end (date)
//	J  health (free text: "Healthy" | "Watch" | "At risk" | "New lead" seen
//	   so far, case-insensitive; any value other than "healthy" still counts
//	   as "not healthy" for accounts_at_risk, and each distinct value is its
//	   own group in accounts_by_health — the compute layer never assumes a
//	   closed set)
//	K  primary channel
//	L  next step (free text)
//	M  last interaction (date) — the last-contact date accountColLastContact
//	   reads; per U9, "days since contact" is always recomputed from this at
//	   render time, never trusted from column N
//	N  days since contact (sheet-computed; not read — see M)
//	O  open tickets (integer)
//	P  latest NPS (integer, blank if none)
//	Q  flag (free text, not read by any compute function)
//
// The sheet's own dates render through gsheets.getRange's
// dateTimeRenderOption=FORMATTED_STRING, which formats by the spreadsheet's
// locale/cell format. Confirmed live (2026-09-27): column M renders as
// plain "YYYY-MM-DD" (e.g. "2026-09-24"), matching compute.go's strict
// "2006-01-02" parse exactly — no adjustment needed.
//
// The real workbook also has Contacts, Interactions, Support tickets,
// Feedback, Reviews and Vendors tabs — none read by this connector. Reviews
// in particular has real per-review data (Product Hunt, G2, Reddit, an app
// review site) that could retire the Clients workspace's permanent
// "not_connected" Reviews tile (Slice UI Phase 5d), but reading a second tab
// is new connector surface beyond the owner's U3-A approval for Accounts
// only, so it isn't read here without a separate decision.
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

// customersSpreadsheetID is Renaissance_Customers, uploaded by the owner as
// a native Google Sheet and shared Viewer with water.google/ceo
// (2026-09-27) — live read access confirmed directly against the Sheets API
// with Water's own stored credential before this was filled in (Summary!
// A1:H12, 12 rows).
const customersSpreadsheetID = "1lfYCxuRw22yfqx75dUCFPQzlWCr84GI47XLBY8h19KU"

// customersConfigurePrefix is the sentinel prefix Invoke checks for. A
// test overrides customersSpreadsheetID (via NewCustomersWithOptions) to
// something else entirely, so this checks the prefix, not equality with
// the specific placeholder string above.
const customersConfigurePrefix = "CONFIGURE_ME_"

// customersLookup is the one named function's fixed tab and A1 range, the
// same shape gsheets.go's own `lookup` type has (kept separate so this
// file has no dependency on gsheets.go's lookups map, which is keyed by
// company_finance's own function names).
var customersLookup = lookup{tab: "Accounts", rangeA1: "A6:Q60"}

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

// NewCustomers builds the production connector, against the owner's real
// Renaissance_Customers spreadsheet id (customersSpreadsheetID, filled in
// 2026-09-27).
func NewCustomers() *Customers { return &Customers{spreadsheetID: customersSpreadsheetID} }

// NewCustomersWithOptions builds a connector against a test double, and
// optionally a spreadsheet id override so a test can exercise either path
// (an empty id keeps whatever customersSpreadsheetID currently is; pass an
// explicit "CONFIGURE_ME_..."-prefixed id to exercise
// ErrCustomersNotConfigured instead, since the production constant is no
// longer a placeholder). This differs from gsheets.NewWithOptions, which
// never needs to override spreadsheetID, because that connector's id has
// been real since it was written.
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
