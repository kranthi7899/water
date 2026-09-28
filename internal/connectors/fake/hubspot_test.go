package fake_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"water/internal/connectors/fake"
	"water/internal/gate"
	"water/internal/store"
)

const hubspotManifest = `
id: t
name: T
usage: {window: 1h, model_calls: 10}
connectors:
  - name: hubspot
    functions:
      - {name: list_deals, level: R}
      - {name: list_contacts, level: R}
`

func TestHubSpotFunctionsSchema(t *testing.T) {
	h := fake.NewHubSpot(fake.DefaultHubSpotDeals(), fake.DefaultHubSpotContacts())
	for _, f := range h.Functions() {
		if f.Level != "R" || !f.External {
			t.Fatalf("%s: level=%s external=%v, want R/true", f.Name, f.Level, f.External)
		}
		b, _ := json.Marshal(f.Schema)
		if !strings.Contains(string(b), `"additionalProperties":false`) {
			t.Fatalf("%s schema: %s", f.Name, b)
		}
		if err := f.Schema.Validate(map[string]any{"query": "acme"}); err != nil {
			t.Fatalf("%s: valid args rejected: %v", f.Name, err)
		}
		if err := f.Schema.Validate(map[string]any{"nope": 1}); err == nil {
			t.Fatalf("%s: unknown argument accepted", f.Name)
		}
	}
}

func TestHubSpotNormalizeDealsAndContacts(t *testing.T) {
	h := fake.NewHubSpot(fake.DefaultHubSpotDeals(), fake.DefaultHubSpotContacts())

	dealRaw, err := json.Marshal(fake.DefaultHubSpotDeals())
	if err != nil {
		t.Fatal(err)
	}
	recs, err := h.Normalize("list_deals", dealRaw)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != len(fake.DefaultHubSpotDeals()) {
		t.Fatalf("records: %d, want %d", len(recs), len(fake.DefaultHubSpotDeals()))
	}
	var sawAcme bool
	for _, r := range recs {
		tx, ok := r.(*store.Transaction)
		if !ok {
			t.Fatalf("deal did not normalize to *store.Transaction: %T", r)
		}
		if !tx.External || tx.Currency != "USD" || tx.PostedAt.IsZero() {
			t.Fatalf("transaction: %+v", tx)
		}
		if tx.Counterparty == "Acme Robotics" {
			sawAcme = true
			if tx.AmountMinor != 4_200_000 {
				t.Fatalf("Acme Robotics deal amount = %d, want 4200000 ($42,000)", tx.AmountMinor)
			}
		}
	}
	if !sawAcme {
		t.Fatal("expected the seeded Acme Robotics deal")
	}

	contactRaw, err := json.Marshal(fake.DefaultHubSpotContacts())
	if err != nil {
		t.Fatal(err)
	}
	recs, err = h.Normalize("list_contacts", contactRaw)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != len(fake.DefaultHubSpotContacts()) {
		t.Fatalf("records: %d, want %d", len(recs), len(fake.DefaultHubSpotContacts()))
	}
	for _, r := range recs {
		c, ok := r.(*store.Contact)
		if !ok {
			t.Fatalf("contact did not normalize to *store.Contact: %T", r)
		}
		if !c.External || c.Org == "" || c.Email == "" {
			t.Fatalf("contact: %+v", c)
		}
	}
}

func TestHubSpotInvokeThroughTheGateFiltersByQuery(t *testing.T) {
	h := fake.NewHubSpot(fake.DefaultHubSpotDeals(), fake.DefaultHubSpotContacts())
	g := newGateHarness(t, hubspotManifest, h)

	res, err := g.Invoke(context.Background(), gate.Call{Function: "hubspot.list_deals", Args: map[string]any{"query": "meridian"}, Origin: gate.P1, Taint: gate.Clean})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 1 {
		t.Fatalf("meridian query: %d records, want 1", len(res.Records))
	}
	tx := res.Records[0].(*store.Transaction)
	if tx.Counterparty != "Meridian Ventures" {
		t.Fatalf("wrong deal matched: %+v", tx)
	}

	res, err = g.Invoke(context.Background(), gate.Call{Function: "hubspot.list_contacts", Args: map[string]any{"query": "acme"}, Origin: gate.P1, Taint: gate.Clean})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 1 {
		t.Fatalf("acme contact query: %d records, want 1", len(res.Records))
	}
}
