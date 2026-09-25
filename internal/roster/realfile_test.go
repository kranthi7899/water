package roster

import (
	"context"
	"testing"
	"time"

	"water"
	"water/internal/store"
)

// TestLoadRealPeopleYAML loads the actual embedded twins/ceo/seed/people.yaml
// (not a test fixture): the strongest guarantee this package's cross-
// validation actually accepts the file the CEO twin ships with, including
// the real file's YAML null identity values (email/slack), multi-team
// people (also_on_teams), and every link kind at once.
func TestLoadRealPeopleYAML(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	if err := Load(ctx, water.TwinsFS(), "ceo", st); err != nil {
		t.Fatal(err)
	}

	// A null YAML identity value (email, slack) must read back as an
	// absent identity, not panic or corrupt the JSON blob.
	kranthi, err := store.Get[store.Person](ctx, st, "seed", "kranthi")
	if err != nil {
		t.Fatal(err)
	}
	if kranthi.Identity("linear_owner_label") != "Kranthi" {
		t.Fatalf("kranthi linear_owner_label = %q, want Kranthi", kranthi.Identity("linear_owner_label"))
	}
	if kranthi.Identity("email") != "" {
		t.Fatalf("kranthi email = %q, want empty (YAML null)", kranthi.Identity("email"))
	}

	// avery is on operations (home) plus yt-recamendo and voice-text
	// (also_on_teams): three member_of edges.
	teams, err := st.LinksFrom(ctx, "person", "avery", store.LinkMemberOf)
	if err != nil {
		t.Fatal(err)
	}
	if len(teams) != 3 {
		t.Fatalf("avery member_of = %+v, want 3 teams", teams)
	}

	// priya owns two clients, both via the clients' own owner field.
	owns, err := st.LinksFrom(ctx, "person", "priya", store.LinkOwnsClient)
	if err != nil {
		t.Fatal(err)
	}
	if len(owns) != 2 {
		t.Fatalf("priya owns_client = %+v, want 2", owns)
	}

	// nimbus-ai's owner (sam) never lists owns_clients themselves — this
	// must still load without error (Parse's cross-check is directional:
	// it only requires agreement when a person DOES claim ownership).
	nimbus, err := store.Get[store.Client](ctx, st, "seed", "nimbus-ai")
	if err != nil || nimbus.Status != "design_partner" {
		t.Fatalf("nimbus-ai = %+v, %v", nimbus, err)
	}

	// theo is fully (1.0) allocated to ingestion-v2, which starts
	// 2026-08-18. Four weeks later, hours invested = 1.0 * 40 * 4 = 160.
	hours, err := PersonProjectHours(ctx, st, "theo", "ingestion-v2", time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if hours != 160 {
		t.Fatalf("theo's ingestion-v2 hours at +4wk = %v, want 160", hours)
	}

	// Resolving a real Linear Owner label end to end.
	p, err := PersonByIdentity(ctx, st, "linear_owner_label", "Nina")
	if err != nil || p.Meta.SourceID != "nina" {
		t.Fatalf("PersonByIdentity(Nina) = %+v, %v", p, err)
	}
}
