package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"water/internal/connectors"
	"water/internal/gate/permit"
	"water/internal/store"
	"water/internal/twins"
)

// ---- hubspot ----
//
// A demo-only, in-memory stand-in for a HubSpot connector. Same posture as
// github.go and linear.go: no network call, no OAuth, no API token.

// HubSpotDeal is one fake CRM deal.
type HubSpotDeal struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Stage       string `json:"stage"` // e.g. Proposal Sent, Negotiation, Closed Won, Follow-up needed
	AmountCents int64  `json:"amount_cents"`
	CloseDate   string `json:"close_date"` // RFC 3339
	Owner       string `json:"owner"`
	Company     string `json:"company"`
}

// HubSpotContact is one fake CRM contact.
type HubSpotContact struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Email   string `json:"email"`
	Phone   string `json:"phone,omitempty"`
	Company string `json:"company"`
	Title   string `json:"title"`
}

// DefaultHubSpotDeals is the canonical demo seed: realistic, clearly-
// fictional deals and dollar amounts. "Meridian Ventures" is the investor
// relationship investor_request.yaml's demo variant pulls (docs/demo.md).
func DefaultHubSpotDeals() []HubSpotDeal {
	return []HubSpotDeal{
		{ID: "deal-1001", Name: "Acme Robotics — annual plan renewal", Stage: "Negotiation", AmountCents: 4_200_000, CloseDate: "2026-10-08T00:00:00Z", Owner: "you", Company: "Acme Robotics"},
		{ID: "deal-1002", Name: "Northwind Logistics — pilot expansion", Stage: "Proposal Sent", AmountCents: 1_850_000, CloseDate: "2026-10-04T00:00:00Z", Owner: "you", Company: "Northwind Logistics"},
		{ID: "deal-1003", Name: "Bluebird Health — proof of concept", Stage: "Closed Won", AmountCents: 900_000, CloseDate: "2026-09-01T00:00:00Z", Owner: "you", Company: "Bluebird Health"},
		{ID: "deal-1004", Name: "Meridian Ventures — Series B follow-on discussion", Stage: "Follow-up needed", AmountCents: 300_000_000, CloseDate: "2026-10-15T00:00:00Z", Owner: "you", Company: "Meridian Ventures"},
	}
}

// DefaultHubSpotContacts is the canonical demo seed of contacts.
func DefaultHubSpotContacts() []HubSpotContact {
	return []HubSpotContact{
		{ID: "contact-1", Name: "Elena Cross", Email: "elena.cross@meridianvc.example", Phone: "+1-555-0101", Company: "Meridian Ventures", Title: "Partner"},
		{ID: "contact-2", Name: "Marcus Webb", Email: "marcus.webb@acmerobotics.example", Phone: "+1-555-0102", Company: "Acme Robotics", Title: "VP Engineering"},
		{ID: "contact-3", Name: "Sofia Alvarez", Email: "sofia.alvarez@northwindlogistics.example", Phone: "+1-555-0103", Company: "Northwind Logistics", Title: "Director of Operations"},
		{ID: "contact-4", Name: "Tomas Reyes", Email: "tomas.reyes@bluebirdhealth.example", Phone: "+1-555-0104", Company: "Bluebird Health", Title: "CTO"},
	}
}

// dealOutput is what list_deals actually returns to the model. AmountUSD is
// computed here, in code, from AmountCents — a model reading a raw
// "amount_cents" integer has no reliable reason to divide by 100 before
// treating it as a dollar figure, and in practice doesn't: it read
// amount_cents:4200000 as "$4.2M" instead of $42,000. AmountCents stays in
// the payload (Normalize still reads it for store.Transaction.AmountMinor),
// but the model is never the one doing that division.
type dealOutput struct {
	HubSpotDeal
	AmountUSD float64 `json:"amount_usd"`
}

// HubSpot is the fake connector.
type HubSpot struct {
	mu       sync.Mutex
	deals    []HubSpotDeal
	contacts []HubSpotContact
}

// NewHubSpot builds the connector from explicit seed data.
func NewHubSpot(deals []HubSpotDeal, contacts []HubSpotContact) *HubSpot {
	return &HubSpot{deals: deals, contacts: contacts}
}

func (*HubSpot) Name() string                 { return "hubspot" }
func (*HubSpot) Credential() (string, string) { return "", "" }

func (*HubSpot) Functions() []connectors.Function {
	return []connectors.Function{
		{Name: "list_deals", Description: "List CRM deals, optionally filtered by keyword.", Level: twins.R, Risk: connectors.RiskLow, External: true,
			Schema: connectors.Schema{Properties: map[string]connectors.Property{"query": str("keyword filter over deal name/company")}}},
		{Name: "list_contacts", Description: "List CRM contacts, optionally filtered by keyword.", Level: twins.R, Risk: connectors.RiskLow, External: true,
			Schema: connectors.Schema{Properties: map[string]connectors.Property{"query": str("keyword filter over name/company")}}},
	}
}

func (h *HubSpot) Invoke(_ context.Context, p permit.Permit) (json.RawMessage, error) {
	call, err := p.Open()
	if err != nil {
		return nil, err
	}
	query := strings.ToLower(strings.TrimSpace(arg(call.Args, "query")))
	h.mu.Lock()
	defer h.mu.Unlock()
	switch call.Function {
	case "list_deals":
		var out []HubSpotDeal
		for _, d := range h.deals {
			if query != "" && !matchesAny(query, strings.ToLower(d.Name), strings.ToLower(d.Company), strings.ToLower(d.Stage)) {
				continue
			}
			out = append(out, d)
		}
		views := make([]dealOutput, len(out))
		for i, d := range out {
			views[i] = dealOutput{HubSpotDeal: d, AmountUSD: float64(d.AmountCents) / 100}
		}
		return json.Marshal(views)
	case "list_contacts":
		var out []HubSpotContact
		for _, c := range h.contacts {
			if query != "" && !matchesAny(query, strings.ToLower(c.Name), strings.ToLower(c.Company), strings.ToLower(c.Title)) {
				continue
			}
			out = append(out, c)
		}
		return json.Marshal(out)
	}
	return nil, fmt.Errorf("hubspot: unknown function %q", call.Function)
}

// Normalize maps deals onto store.Transaction and contacts onto
// store.Contact. store.Transaction has no dedicated pipeline-stage field, so
// Account (normally a bank/payment account name) carries the deal's Stage
// instead — the closest fit among the fields the shared schema offers; see
// docs/EVOLUTION_PLAN.md's demo-slice log entry. Both are External: true:
// a CRM deal or contact describes someone else's organization, not the
// CEO's own writing.
func (h *HubSpot) Normalize(fn string, raw json.RawMessage) ([]store.Record, error) {
	switch fn {
	case "list_deals":
		var deals []HubSpotDeal
		if err := json.Unmarshal(raw, &deals); err != nil {
			return nil, err
		}
		var out []store.Record
		for _, d := range deals {
			close, _ := time.Parse(time.RFC3339, d.CloseDate)
			out = append(out, &store.Transaction{
				Meta:         store.Meta{Source: h.Name(), SourceID: d.ID, External: true},
				Account:      d.Stage,
				AmountMinor:  d.AmountCents,
				Currency:     "USD",
				Counterparty: d.Company,
				Description:  d.Name,
				PostedAt:     close.UTC(),
			})
		}
		return out, nil
	case "list_contacts":
		var contacts []HubSpotContact
		if err := json.Unmarshal(raw, &contacts); err != nil {
			return nil, err
		}
		var out []store.Record
		for _, c := range contacts {
			out = append(out, &store.Contact{
				Meta:  store.Meta{Source: h.Name(), SourceID: c.ID, External: true},
				Name:  c.Name,
				Email: c.Email,
				Phone: c.Phone,
				Org:   c.Company,
				Title: c.Title,
			})
		}
		return out, nil
	}
	return nil, nil
}
