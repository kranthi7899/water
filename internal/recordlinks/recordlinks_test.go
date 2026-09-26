package recordlinks

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"water/internal/store"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// seedPerson writes a roster person the way roster.Write does (source
// "seed", identities as a JSON object).
func seedPerson(t *testing.T, st *store.Store, id, identities string) {
	t.Helper()
	if err := st.Upsert(context.Background(), &store.Person{Meta: store.Meta{Source: "seed", SourceID: id}, Name: id, Identities: identities}); err != nil {
		t.Fatal(err)
	}
}

func TestPersonByAddressResolvesOnlyExactEmailIdentities(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedPerson(t, st, "dana", `{"email":"dana@example.com","linear_owner_label":"Dana"}`)
	seedPerson(t, st, "kranthi", `{"email":null,"linear_owner_label":"Kranthi"}`)

	for raw, want := range map[string]string{
		"dana@example.com":             "dana",
		`"Dana R" <dana@example.com>`:  "dana",
		"  Dana <dana@example.com>  ":  "dana",
		"Dana@Example.com":             "dana", // the lowercase form is tried too
		"dana@example.org":             "",
		"Dana":                         "", // a display name alone is never matched
		"Kranthi":                      "", // nor a different identity key's value
		"":                             "",
		"not an address":               "",
		"dana@example.com, x@y.z":      "", // two addresses: not one sender
		"<dana@example.com> trailing!": "",
	} {
		id, ok, err := PersonByAddress(ctx, st, raw)
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if ok != (want != "") || id != want {
			t.Errorf("PersonByAddress(%q) = %q, %v; want %q", raw, id, ok, want)
		}
	}
}

func TestInvolveAddressesWritesResolvedOnlyAndIsIdempotent(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedPerson(t, st, "dana", `{"email":"dana@example.com"}`)
	seedPerson(t, st, "sam", `{"email":"sam@example.com"}`)

	raws := []string{"sam@example.com", "stranger@elsewhere.com", "Dana <dana@example.com>", "dana@example.com"}
	for run := 0; run < 2; run++ {
		got, err := InvolveAddresses(ctx, st, "meeting", "mtg_1", raws)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, []string{"dana", "sam"}) {
			t.Fatalf("run %d: linked %v, want [dana sam]", run, got)
		}
		links, err := st.LinksOf(ctx, "meeting", "mtg_1")
		if err != nil {
			t.Fatal(err)
		}
		if len(links) != 2 {
			t.Fatalf("run %d: links = %+v, want exactly 2", run, links)
		}
		for _, l := range links {
			if l.Kind != store.LinkInvolves || l.FromType != "meeting" || l.ToType != "person" {
				t.Fatalf("run %d: unexpected link %+v", run, l)
			}
		}
	}

	// Nothing resolves: nothing is written.
	if _, err := InvolveAddresses(ctx, st, "meeting", "mtg_2", []string{"stranger@elsewhere.com", "Dana"}); err != nil {
		t.Fatal(err)
	}
	if links, _ := st.LinksOf(ctx, "meeting", "mtg_2"); len(links) != 0 {
		t.Fatalf("unresolved identities wrote %+v", links)
	}
}

func TestLinkDecisionUsesTheStoredSourceMessageSender(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	seedPerson(t, st, "dana", `{"email":"dana@example.com"}`)
	for _, m := range []store.Message{
		{Meta: store.Meta{Source: "gmail", SourceID: "m1"}, From: `Dana <dana@example.com>`},
		{Meta: store.Meta{Source: "gmail", SourceID: "m2"}, From: "stranger@elsewhere.com"},
	} {
		m := m
		if err := st.Upsert(ctx, &m); err != nil {
			t.Fatal(err)
		}
	}

	got, err := LinkDecision(ctx, st, "card-1", []string{"gmail:m1", "gdrive:doc-9", "gmail:missing", "no-colon"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"dana"}) {
		t.Fatalf("linked %v, want [dana]", got)
	}
	want := []store.Link{{Kind: store.LinkInvolves, FromType: "decision", FromID: "card-1", ToType: "person", ToID: "dana"}}
	if links, _ := st.LinksOf(ctx, "decision", "card-1"); !reflect.DeepEqual(links, want) {
		t.Fatalf("links = %+v, want %+v", links, want)
	}

	got, err = LinkDecision(ctx, st, "card-2", []string{"gmail:m2"})
	if err != nil || len(got) != 0 {
		t.Fatalf("unknown sender linked %v, %v", got, err)
	}
	if links, _ := st.LinksOf(ctx, "decision", "card-2"); len(links) != 0 {
		t.Fatalf("unknown sender wrote %+v", links)
	}
}

func TestCopyLinksCopiesOnlyOutgoingInvolvesAndForProject(t *testing.T) {
	st := newStore(t)
	ctx := context.Background()
	for _, l := range []store.Link{
		{Kind: store.LinkInvolves, FromType: "meeting", FromID: "mtg_1", ToType: "person", ToID: "dana"},
		{Kind: store.LinkForProject, FromType: "meeting", FromID: "mtg_1", ToType: "project", ToID: "halcyon-rollout"},
		{Kind: store.LinkInWorkspace, FromType: "meeting", FromID: "mtg_1", ToType: "workspace", ToID: "ws_1"},
		{Kind: store.LinkAbout, FromType: "thread", FromID: "thr_other", ToType: "meeting", ToID: "mtg_1"},
	} {
		if err := st.AddLink(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	for run := 0; run < 2; run++ {
		if err := CopyLinks(ctx, st, "meeting", "mtg_1", "thread", "thr_1"); err != nil {
			t.Fatal(err)
		}
	}
	links, err := st.LinksOf(ctx, "thread", "thr_1")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]store.Link{
		store.LinkInvolves:   {Kind: store.LinkInvolves, FromType: "thread", FromID: "thr_1", ToType: "person", ToID: "dana"},
		store.LinkForProject: {Kind: store.LinkForProject, FromType: "thread", FromID: "thr_1", ToType: "project", ToID: "halcyon-rollout"},
	}
	if len(links) != len(want) {
		t.Fatalf("links = %+v, want exactly %d", links, len(want))
	}
	for _, l := range links {
		if want[l.Kind] != l {
			t.Fatalf("unexpected link %+v", l)
		}
	}
}
