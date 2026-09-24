package decisions

import (
	"context"
	"encoding/csv"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"

	"water/internal/store"
)

func init() {
	RegisterCompute("internal://budget_request.compute_runway", computeRunway)
}

// budgetSpreadsheetNeed is the need name budget_request.yaml gives its
// gdrive.read_file fetch of the runway spreadsheet; computeRunway reads that
// need's already-resolved records rather than fetching anything itself (a
// compute function has no gate access, by design).
const budgetSpreadsheetNeed = "budget_spreadsheet"

// computeRunwaySource is every figure and evidence item this function
// produces; it is code, never a model completion.
const computeRunwaySource = "code:budget_request.compute_runway"

// computeRunway parses the exported budget spreadsheet (CSV: a header row
// naming a cash column and a monthly-burn column, one data row per month)
// into numbers and computes runway (cash / burn) and the current burn rate
// by code. Anything it can't make sense of — no spreadsheet resolved yet, no
// records, an unreadable or headerless export, no numeric row, a zero burn
// rate — returns an empty result (missing_info via the normal readiness
// rule), never an error or a panic.
//
// It parses the read's full Content, never its Excerpt: the excerpt is cut
// at a fixed rune count, so its "last row" is whichever row happened to fit
// (often cut mid-number, "45000" read as "45"), not the latest month. A
// Truncated export has lost its newest rows (the sheet runs oldest to
// newest), so it is treated as unreadable rather than silently reporting an
// old month as current.
func computeRunway(_ context.Context, in ComputeInput) (ComputeResult, error) {
	res, ok := in.Resolved[budgetSpreadsheetNeed]
	if !ok || len(res.Records) == 0 {
		return ComputeResult{}, nil
	}
	doc, ok := res.Records[0].(*store.Document)
	if !ok || doc.Truncated || strings.TrimSpace(doc.Content) == "" {
		return ComputeResult{}, nil
	}
	cash, burn, rows, ok := parseBudgetCSV(doc.Content)
	if !ok {
		return ComputeResult{}, nil
	}
	runway := cash / burn
	if math.IsNaN(runway) || math.IsInf(runway, 0) || runway < 0 {
		// Unreachable given parseBudgetCSV's checks; a figure that is not a
		// real, finite runway must never reach a card as "sourced".
		return ComputeResult{}, nil
	}
	return ComputeResult{
		Figures: map[string]Figure{
			"cash_on_hand":  {Value: round1(cash), Source: computeRunwaySource},
			"monthly_burn":  {Value: round1(burn), Source: computeRunwaySource},
			"runway_months": {Value: round1(runway), Source: computeRunwaySource},
		},
		Evidence: []Evidence{{
			Text: fmt.Sprintf("Parsed %d month(s) from the budget spreadsheet: latest cash $%s, burn $%s/mo, runway %.1f month(s).",
				rows, money(cash), money(burn), runway),
			Source: computeRunwaySource,
		}},
		// The spreadsheet is someone else's Drive content, not the CEO's own
		// research: the card it feeds must be marked untrusted.
		External: true,
	}, nil
}

// cashHeaders and burnHeaders are the normalized header names the parser
// recognizes for each column, kept small and explicit rather than guessed.
var (
	cashHeaders = map[string]bool{"cash": true, "cashonhand": true, "cashbalance": true, "cashinbank": true, "balance": true}
	burnHeaders = map[string]bool{"burn": true, "monthlyburn": true, "burnrate": true, "netburn": true}
)

// parseBudgetCSV finds the cash and burn columns by header and returns the
// last data row where both parse as numbers — the most recent month, since
// the sheet is expected oldest-to-newest. ok is false for anything that
// isn't a clean, well-formed sheet with at least one usable row, a
// positive burn (a zero or negative burn makes "runway" undefined, not
// infinite) and a non-negative cash balance.
func parseBudgetCSV(s string) (cash, burn float64, rows int, ok bool) {
	// Sniff the delimiter from the header line only: a data row's money
	// values ("$120,000") may contain commas even in a tab-delimited sheet.
	header := s
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		header = s[:i]
	}
	delim := ','
	if !strings.Contains(header, ",") && strings.Contains(header, "\t") {
		delim = '\t'
	}
	r := csv.NewReader(strings.NewReader(s))
	r.Comma = delim
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = true
	records, err := r.ReadAll()
	if err != nil || len(records) < 2 {
		return 0, 0, 0, false
	}
	cashIdx, burnIdx := -1, -1
	for i, h := range records[0] {
		switch {
		case cashHeaders[normalizeHeader(h)]:
			cashIdx = i
		case burnHeaders[normalizeHeader(h)]:
			burnIdx = i
		}
	}
	if cashIdx < 0 || burnIdx < 0 {
		return 0, 0, 0, false
	}
	for _, row := range records[1:] {
		if cashIdx >= len(row) || burnIdx >= len(row) {
			continue
		}
		c, cErr := parseMoney(row[cashIdx])
		b, bErr := parseMoney(row[burnIdx])
		if cErr != nil || bErr != nil {
			continue
		}
		cash, burn = c, b
		rows++
	}
	if rows == 0 || !(burn > 0) || !(cash >= 0) {
		return 0, 0, 0, false
	}
	return cash, burn, rows, true
}

// normalizeHeader keeps only letters and digits, lowercased, so "Cash ($)",
// "cash_on_hand" and "Cash On Hand" all match the same header name.
func normalizeHeader(h string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(h) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// parseMoney reads "$1,234.50", "1234.5" or "1234" as a finite float. Only
// digits, one leading '-' and a '.' are accepted: strconv.ParseFloat alone
// would also take "NaN", "Inf", "infinity", hex and exponents, and a NaN
// slips past every "<= 0" check downstream. The cell is someone else's
// Drive content, so anything else is not an amount.
func parseMoney(s string) (float64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "$")
	s = strings.ReplaceAll(s, ",", "")
	if s == "" {
		return 0, fmt.Errorf("empty amount")
	}
	for i, r := range s {
		if !(r >= '0' && r <= '9') && r != '.' && !(r == '-' && i == 0) {
			return 0, fmt.Errorf("not a plain amount: %q", s)
		}
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("not a finite amount: %q", s)
	}
	return f, nil
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

func money(f float64) string { return strconv.FormatFloat(f, 'f', 0, 64) }
