package needsyou

import (
	"context"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/decisions"
	"water/internal/store"
)

// TestItemOriginThreeCases pins itemOrigin's priority order and its three
// documented outcomes directly, with no card or envelope involved.
func TestItemOriginThreeCases(t *testing.T) {
	cases := []struct {
		name                         string
		sourceCardTitle, requestedBy string
		untrusted                    bool
		want                         string
	}{
		{"decision-sourced", "Meridian renewal", "", false, "From Meridian renewal"},
		{"person-request-sourced", "", "Lee", false, "Requested by Lee"},
		{"untrusted with no better origin", "", "", true, "Built from an outside email"},
		{"none of the three", "", "", false, ""},
		{"source card wins over requester", "Meridian renewal", "Lee", true, "From Meridian renewal"},
		{"requester wins over untrusted", "", "Lee", true, "Requested by Lee"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := itemOrigin(c.sourceCardTitle, c.requestedBy, c.untrusted); got != c.want {
				t.Errorf("itemOrigin(%q, %q, %v) = %q, want %q", c.sourceCardTitle, c.requestedBy, c.untrusted, got, c.want)
			}
		})
	}
}

// TestItemFromCardSetsOriginFromUntrustedOnly: a decision card has no
// source-card or requester concept, so its Origin is "" unless Untrusted.
func TestItemFromCardSetsOriginFromUntrustedOnly(t *testing.T) {
	now := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	trusted := itemFromCard(&decisions.Card{ID: "c1", Lead: "Renew Meridian"}, now)
	if trusted.Origin != "" {
		t.Errorf("trusted card Origin = %q, want empty", trusted.Origin)
	}
	untrusted := itemFromCard(&decisions.Card{ID: "c2", Lead: "Renew Meridian", Untrusted: true}, now)
	if untrusted.Origin != "Built from an outside email" {
		t.Errorf("untrusted card Origin = %q, want the built-from-outside-email note", untrusted.Origin)
	}
	if untrusted.Priority != trusted.Priority {
		t.Errorf("Origin must not affect Priority: %v vs %v", untrusted.Priority, trusted.Priority)
	}
}

// TestItemFromEnvelopeOrigin covers an agent-draft approval's decision
// origin and a person-request approval's requester origin.
func TestItemFromEnvelopeOrigin(t *testing.T) {
	now := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	draft := itemFromEnvelope(approvals.Envelope{ID: "env_1", Action: "gmail.send_message", SourceCardID: "card-meridian"}, now, "Meridian renewal", "")
	if draft.Origin != "From Meridian renewal" {
		t.Errorf("agent-draft Origin = %q, want %q", draft.Origin, "From Meridian renewal")
	}
	if draft.Priority != ApprovalPriority(false, false, nil, now) {
		t.Errorf("Origin wiring must leave Priority computed exactly as before")
	}

	req := itemFromEnvelope(approvals.Envelope{ID: "env_2", Action: "requests.respond", OriginKind: approvals.OriginKindPersonRequest, RequestedBy: "lee"}, now, "", "Lee Chen")
	if req.Origin != "Requested by Lee Chen" {
		t.Errorf("person-request Origin = %q, want %q", req.Origin, "Requested by Lee Chen")
	}
	if req.OriginKind != approvals.OriginKindPersonRequest {
		t.Errorf("OriginKind = %q, want %q", req.OriginKind, approvals.OriginKindPersonRequest)
	}
}

// TestRequesterNameResolvesViaRoster exercises Compute end to end: a
// person-request approval's RequestedBy resolves to the roster person's
// Name through the same store a real daemon loads roster.Load into.
func TestRequesterNameResolvesViaRoster(t *testing.T) {
	st, q := harness(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC)
	q.Now = func() time.Time { return now.Add(-time.Hour) }

	if err := st.Upsert(ctx, &store.Person{Meta: store.Meta{Source: "seed", SourceID: "lee"}, Name: "Lee Chen"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.ProposeRequest(ctx, approvals.Envelope{
		RequestedBy: "lee", Origin: "test", Payload: map[string]any{"request_id": "r1", "answer": "ok"},
	}); err != nil {
		t.Fatal(err)
	}
	q.Now = func() time.Time { return now }

	items, err := Compute(ctx, &fakeSource{}, q, st, now, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("len(items) = %d, want 1: %+v", len(items), items)
	}
	if items[0].Origin != "Requested by Lee Chen" {
		t.Errorf("Origin = %q, want %q", items[0].Origin, "Requested by Lee Chen")
	}

	// An unresolvable RequestedBy (no such roster person) falls back to no
	// origin line at all, never a fabricated name.
	if got := requesterName(ctx, st, "nobody-such-id"); got != "" {
		t.Errorf("requesterName for an unknown id = %q, want empty", got)
	}
}
