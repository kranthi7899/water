package meetings

import (
	"context"
	"testing"
	"time"

	"water/internal/store"
)

// TestStartLinksResolvedAttendees: a session started from a stored calendar
// event gets meeting -> involves -> person for each attendee the roster
// resolves by email, and nothing for the rest (docs/slices/V.md D6).
func TestStartLinksResolvedAttendees(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	for id, ids := range map[string]string{
		"dana": `{"email":"dana@example.com"}`,
		"sam":  `{"email":"sam@example.com"}`,
		"theo": `{"linear_owner_label":"Theo"}`,
	} {
		if err := m.st.Upsert(ctx, &store.Person{Meta: store.Meta{Source: "seed", SourceID: id}, Name: id, Identities: ids}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	if err := m.st.Upsert(ctx, &store.Event{
		Meta: store.Meta{Source: "gcal", SourceID: "evt-1"}, Title: "Halcyon sync", StartAt: now, EndAt: now.Add(time.Hour),
		Attendees: []string{"dana@example.com", "stranger@elsewhere.com", "Theo", "sam@example.com"},
	}); err != nil {
		t.Fatal(err)
	}

	s, err := m.Start(ctx, "evt-1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"dana": true, "sam": true}
	check := func(when string) {
		t.Helper()
		links, err := m.st.LinksOf(ctx, "meeting", s.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(links) != len(want) {
			t.Fatalf("%s: links = %+v, want %d involves edges", when, links, len(want))
		}
		for _, l := range links {
			if l.Kind != store.LinkInvolves || l.FromType != "meeting" || l.FromID != s.ID || l.ToType != "person" || !want[l.ToID] {
				t.Fatalf("%s: unexpected link %+v", when, l)
			}
		}
	}
	check("after start")

	// Re-running the writer adds no duplicate.
	if err := m.LinkAttendees(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	check("after re-run")
}

func TestStartWritesNoLinkWhenNothingResolves(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := m.st.Upsert(ctx, &store.Event{
		Meta: store.Meta{Source: "gcal", SourceID: "evt-2"}, StartAt: now, EndAt: now.Add(time.Hour),
		Attendees: []string{"stranger@elsewhere.com"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, eventID := range []string{"evt-2", "evt-not-stored", ""} {
		s, err := m.Start(ctx, eventID)
		if err != nil {
			t.Fatalf("%q: %v", eventID, err)
		}
		if links, err := m.st.LinksOf(ctx, "meeting", s.ID); err != nil || len(links) != 0 {
			t.Fatalf("%q: links = %+v, %v; want none", eventID, links, err)
		}
	}
	if err := m.LinkAttendees(ctx, "mtg_nope"); err == nil {
		t.Fatal("LinkAttendees on an unknown session: want an error")
	}
}
