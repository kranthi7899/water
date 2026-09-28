package store

import "encoding/json"

// FinanceFigure is one normalized record from the company_finance connector
// (internal/connectors/google/gsheets): a specific tab and A1 range read
// live from the company's real financial model. Source is always
// "company_finance"; SourceID is "<Tab>!<Range>" (e.g. "Cash & runway!
// B19:B21"), which doubles as exactly the citation a decision card's
// evidence needs — see Ref() in internal/decisions/record.go, which builds
// an Evidence.Source as "<Source>:<SourceID>".
type FinanceFigure struct {
	Meta
	Tab     string `db:"tab"`
	RangeA1 string `db:"range_a1"`
	// Values is a JSON object whose shape varies per function — e.g.
	// cash_position's is {"cash_usd":N,"burn_usd":N,"runway_months":N}, all
	// numeric. Use Decode to read it into a typed struct (each function's
	// own caller already knows its exact shape) rather than a lossy
	// single-key string getter, since these are numbers, not identities.
	Values string `db:"values_json"`
}

func (*FinanceFigure) Table() string { return "finance_figures" }

// Decode unmarshals Values into out (typically a pointer to the specific
// struct the producing function used). Returns the error verbatim so a
// caller can distinguish "no data" from "wrong shape" if it matters.
func (f FinanceFigure) Decode(out any) error {
	return json.Unmarshal([]byte(f.Values), out)
}
