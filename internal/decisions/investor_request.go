package decisions

import (
	"context"
	"fmt"
	"math"
	"time"

	"water/internal/store"
)

func init() {
	RegisterCompute("internal://investor_request.compute_deal_health", computeDealHealth)
}

// investorDealNeed is the need name the demo variant of investor_request.yaml
// (twins/ceo-demo/decisions/investor_request.yaml) gives its hubspot.list_deals
// fetch; computeDealHealth reads that need's already-resolved records rather
// than fetching anything itself (a compute function has no gate access, by
// design — see compute.go).
const investorDealNeed = "investor_deal"

// computeDealHealthSource is every figure and evidence item this function
// produces; it is code, never a model completion.
const computeDealHealthSource = "code:investor_request.compute_deal_health"

// computeDealHealth reads the HubSpot deal(s) resolved for investor_deal (as
// fake.HubSpot.Normalize shapes them: a store.Transaction per deal, Account
// repurposed for the pipeline stage — see internal/connectors/fake/hubspot.go)
// and computes, in code, the deal's dollar amount, its stage, and how many
// days remain (or have passed) until its close date. No spreadsheet or model
// arithmetic is involved: this is a straight unit conversion and a date
// subtraction over an already-normalized record. Anything it cannot make
// sense of — no deal resolved yet, no records, a record with no close
// date — returns an empty result (missing_info via the normal readiness
// rule), never an error or a panic, the same posture computeRunway takes in
// budget_request.go.
func computeDealHealth(_ context.Context, in ComputeInput) (ComputeResult, error) {
	res, ok := in.Resolved[investorDealNeed]
	if !ok || len(res.Records) == 0 {
		return ComputeResult{}, nil
	}
	tx, ok := res.Records[0].(*store.Transaction)
	if !ok || tx.PostedAt.IsZero() {
		return ComputeResult{}, nil
	}
	amount := float64(tx.AmountMinor) / 100
	days := int(math.Round(time.Until(tx.PostedAt).Hours() / 24))
	return ComputeResult{
		Figures: map[string]Figure{
			"deal_amount_usd": {Value: round1(amount), Source: computeDealHealthSource},
			"deal_stage":      {Value: tx.Account, Source: computeDealHealthSource},
			"days_to_close":   {Value: days, Source: computeDealHealthSource},
		},
		Evidence: []Evidence{{
			Text: fmt.Sprintf("HubSpot deal %q with %s: $%s, stage %q, closes in %d day(s).",
				tx.Description, tx.Counterparty, money(amount), tx.Account, days),
			Source: computeDealHealthSource,
		}},
		// A HubSpot deal describes another organization's relationship with
		// the company, not the CEO's own research: the card it feeds must be
		// marked untrusted, the same rule budget_request's Drive sheet gets.
		External: true,
	}, nil
}
