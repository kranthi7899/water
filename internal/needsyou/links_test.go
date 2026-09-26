package needsyou

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"water/internal/decisions"
	"water/internal/store"
)

// TestTickLinksDecisionToResolvedSourceSender: a card that crosses the
// threshold is linked decision -> involves -> person for its source
// message's sender when the roster resolves it; an unresolved sender and a
// below-threshold card get nothing; ticking again adds no duplicate.
func TestTickLinksDecisionToResolvedSourceSender(t *testing.T) {
	st, q := harness(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	if err := st.Upsert(ctx, &store.Person{Meta: store.Meta{Source: "seed", SourceID: "dana"}, Name: "Dana", Identities: `{"email":"dana@example.com"}`}); err != nil {
		t.Fatal(err)
	}
	for id, from := range map[string]string{"m1": "Dana <dana@example.com>", "m2": "stranger@elsewhere.com", "m3": "dana@example.com"} {
		if err := st.Upsert(ctx, &store.Message{Meta: store.Meta{Source: "gmail", SourceID: id}, From: from}); err != nil {
			t.Fatal(err)
		}
	}
	src := &fakeSource{cards: []*decisions.Card{
		{ID: "c-known", Severity: 3, Lead: "known sender", SourceItemIDs: []string{"gmail:m1"}},
		{ID: "c-stranger", Severity: 3, Lead: "unknown sender", SourceItemIDs: []string{"gmail:m2"}},
		{ID: "c-low", Severity: 1, Lead: "below threshold", SourceItemIDs: []string{"gmail:m3"}},
	}}
	svc := NewService(src, q, st, 2, time.Minute)

	for tick := 0; tick < 2; tick++ {
		if err := svc.Tick(ctx, now); err != nil {
			t.Fatal(err)
		}
		links, err := st.LinksOf(ctx, "decision", "c-known")
		if err != nil {
			t.Fatal(err)
		}
		want := store.Link{Kind: store.LinkInvolves, FromType: "decision", FromID: "c-known", ToType: "person", ToID: "dana"}
		if len(links) != 1 || links[0] != want {
			t.Fatalf("tick %d: c-known links = %+v, want exactly %+v", tick, links, want)
		}
		for _, id := range []string{"c-stranger", "c-low"} {
			if links, _ := st.LinksOf(ctx, "decision", id); len(links) != 0 {
				t.Fatalf("tick %d: %s links = %+v, want none", tick, id, links)
			}
		}
	}

	// The kept source ids never leak into Today's JSON.
	b, err := json.Marshal(svc.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var generic []map[string]any
	if err := json.Unmarshal(b, &generic); err != nil {
		t.Fatal(err)
	}
	for _, it := range generic {
		for k := range it {
			if k == "sourceItemIDs" || k == "SourceItemIDs" {
				t.Fatalf("snapshot JSON carries %q: %s", k, b)
			}
		}
	}
}
