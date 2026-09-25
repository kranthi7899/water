package roster

import (
	"context"
	"path/filepath"
	"testing"
	"testing/fstest"

	"water/internal/store"
)

func openTemp(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

const minimalYAML = `
company: {name: Test Co, headcount: 3, blended_hourly_cost_usd: 80}

teams:
  - {id: crawler, name: Crawler, linear_key: CRA}

projects:
  - {id: ingestion-v2, name: Ingestion v2, team: crawler, linear: P-CRA-1, start: 2026-08-18, target: 2026-11-15}

people:
  - id: theo
    name: Theo
    role: Engineer
    home_team: crawler
    leads: [ingestion-v2]
    allocation: {ingestion-v2: 1.0}
    identities: {linear_owner_label: Theo}
  - id: lee
    name: Lee
    role: Lead
    home_team: crawler
    also_on_teams: []
    leads: [crawler]
    allocation: {ingestion-v2: 0.5}
    owns_clients: [atlas-research]
    identities: {linear_owner_label: Lee}

external:
  clients:
    - {id: atlas-research, name: Atlas Research, product: crawler, owner: lee, mrr_usd: 4000}
  vendors:
    - {id: lexicon-archive, name: Lexicon Archive, product: econ-rag, monthly_usd: 2800, renewal: 2026-11-30}
  contacts:
    - {id: alex, name: Alex, role: Candidate, for_team: crawler, stage: final_interview}
`

func TestParseValidDocument(t *testing.T) {
	r, err := Parse([]byte(minimalYAML))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.doc.Teams) != 1 || len(r.doc.Projects) != 1 || len(r.doc.People) != 2 {
		t.Fatalf("doc = %+v", r.doc)
	}
}

func TestParseUnknownFieldRejected(t *testing.T) {
	bad := minimalYAML + "\nsome_unknown_top_level_key: 1\n"
	if _, err := Parse([]byte(bad)); err == nil {
		t.Fatal("expected an error for an unknown top-level key")
	}
}

func TestParseUnknownTeamReferenceRejected(t *testing.T) {
	bad := `
teams: []
projects:
  - {id: p1, name: P1, team: nosuchteam}
people: []
`
	if _, err := Parse([]byte(bad)); err == nil {
		t.Fatal("expected an error for a project naming an unknown team")
	}
}

func TestParseUnknownAllocationProjectRejected(t *testing.T) {
	bad := `
teams: [{id: t1, name: T1}]
projects: []
people:
  - {id: p1, name: P1, home_team: t1, allocation: {nosuchproject: 1.0}}
`
	if _, err := Parse([]byte(bad)); err == nil {
		t.Fatal("expected an error for allocation to an unknown project")
	}
}

func TestParseOwnsClientsMismatchWithClientOwnerRejected(t *testing.T) {
	bad := `
teams: [{id: t1, name: T1}]
projects: []
people:
  - {id: p1, name: P1, home_team: t1, owns_clients: [c1]}
  - {id: p2, name: P2, home_team: t1}
external:
  clients:
    - {id: c1, name: C1, owner: p2}
`
	_, err := Parse([]byte(bad))
	if err == nil {
		t.Fatal("expected an error: p1 claims owns_clients [c1], but c1's own owner is p2")
	}
}

func TestParseDuplicatePersonIDRejected(t *testing.T) {
	bad := `
teams: []
projects: []
people:
  - {id: p1, name: P1}
  - {id: p1, name: P1 again}
`
	if _, err := Parse([]byte(bad)); err == nil {
		t.Fatal("expected an error for a duplicate person id")
	}
}

func TestParseBadDateRejected(t *testing.T) {
	bad := `
teams: []
projects:
  - {id: p1, name: P1, start: "not-a-date"}
people: []
`
	if _, err := Parse([]byte(bad)); err == nil {
		t.Fatal("expected an error for an unparseable start date")
	}
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	st := openTemp(t)
	fsys := fstest.MapFS{} // no twins/ceo-demo/seed/people.yaml at all
	if err := Load(context.Background(), fsys, "ceo-demo", st); err != nil {
		t.Fatalf("Load with no seed file = %v, want nil (roster is optional)", err)
	}
}

func TestLoadMalformedFileFailsLoudly(t *testing.T) {
	st := openTemp(t)
	fsys := fstest.MapFS{
		"twins/ceo/seed/people.yaml": &fstest.MapFile{Data: []byte("not: [valid, yaml, for, this, schema\n")},
	}
	if err := Load(context.Background(), fsys, "ceo", st); err == nil {
		t.Fatal("expected Load to fail loudly on a malformed seed file")
	}
}

func TestLoadWritesRecordsAndLinks(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	fsys := fstest.MapFS{
		"twins/ceo/seed/people.yaml": &fstest.MapFile{Data: []byte(minimalYAML)},
	}
	if err := Load(ctx, fsys, "ceo", st); err != nil {
		t.Fatal(err)
	}

	team, err := store.Get[store.Team](ctx, st, "seed", "crawler")
	if err != nil || team.LinearKey != "CRA" {
		t.Fatalf("team = %+v, %v", team, err)
	}
	proj, err := store.Get[store.Project](ctx, st, "seed", "ingestion-v2")
	if err != nil || proj.LinearProject != "P-CRA-1" {
		t.Fatalf("project = %+v, %v", proj, err)
	}
	theo, err := store.Get[store.Person](ctx, st, "seed", "theo")
	if err != nil || theo.Identity("linear_owner_label") != "Theo" {
		t.Fatalf("person theo = %+v, %v", theo, err)
	}
	client, err := store.Get[store.Client](ctx, st, "seed", "atlas-research")
	if err != nil || client.MRRMinor != 400000 {
		t.Fatalf("client = %+v, %v (mrr_usd 4000 -> 400000 minor units)", client, err)
	}
	vendor, err := store.Get[store.Vendor](ctx, st, "seed", "lexicon-archive")
	if err != nil || vendor.MonthlyMinor != 280000 {
		t.Fatalf("vendor = %+v, %v", vendor, err)
	}
	contact, err := store.Get[store.OrgContact](ctx, st, "seed", "alex")
	if err != nil || contact.Stage != "final_interview" {
		t.Fatalf("contact = %+v, %v", contact, err)
	}

	// member_of: theo -> crawler (home_team)
	mo, err := st.LinksFrom(ctx, "person", "theo", store.LinkMemberOf)
	if err != nil || len(mo) != 1 || mo[0].ToID != "crawler" {
		t.Fatalf("theo member_of = %+v, %v", mo, err)
	}
	// project member_of team: ingestion-v2 -> crawler
	pmo, err := st.LinksFrom(ctx, "project", "ingestion-v2", store.LinkMemberOf)
	if err != nil || len(pmo) != 1 || pmo[0].ToID != "crawler" {
		t.Fatalf("project member_of = %+v, %v", pmo, err)
	}
	// leads: theo -> ingestion-v2 (a project), lee -> crawler (a team) --
	// both resolved to the right ToType automatically.
	theoLeads, err := st.LinksFrom(ctx, "person", "theo", store.LinkLeads)
	if err != nil || len(theoLeads) != 1 || theoLeads[0].ToType != "project" || theoLeads[0].ToID != "ingestion-v2" {
		t.Fatalf("theo leads = %+v, %v", theoLeads, err)
	}
	leeLeads, err := st.LinksFrom(ctx, "person", "lee", store.LinkLeads)
	if err != nil || len(leeLeads) != 1 || leeLeads[0].ToType != "team" || leeLeads[0].ToID != "crawler" {
		t.Fatalf("lee leads = %+v, %v", leeLeads, err)
	}
	// allocated_to, with fraction
	alloc, err := st.LinksFrom(ctx, "person", "lee", store.LinkAllocated)
	if err != nil || len(alloc) != 1 || alloc[0].ToID != "ingestion-v2" || alloc[0].Fraction != 0.5 {
		t.Fatalf("lee allocated_to = %+v, %v", alloc, err)
	}
	// owns_client, written from the client's own owner field
	owns, err := st.LinksFrom(ctx, "person", "lee", store.LinkOwnsClient)
	if err != nil || len(owns) != 1 || owns[0].ToID != "atlas-research" {
		t.Fatalf("lee owns_client = %+v, %v", owns, err)
	}
}

func TestLoadIsIdempotent(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	fsys := fstest.MapFS{
		"twins/ceo/seed/people.yaml": &fstest.MapFile{Data: []byte(minimalYAML)},
	}
	if err := Load(ctx, fsys, "ceo", st); err != nil {
		t.Fatal(err)
	}
	if err := Load(ctx, fsys, "ceo", st); err != nil {
		t.Fatal(err)
	}
	alloc, err := st.LinksFrom(ctx, "person", "lee", store.LinkAllocated)
	if err != nil || len(alloc) != 1 {
		t.Fatalf("alloc after reload = %+v, %v, want exactly one edge (no duplicate)", alloc, err)
	}
}

func TestPersonByIdentityResolvesAndMisses(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	fsys := fstest.MapFS{
		"twins/ceo/seed/people.yaml": &fstest.MapFile{Data: []byte(minimalYAML)},
	}
	if err := Load(ctx, fsys, "ceo", st); err != nil {
		t.Fatal(err)
	}
	p, err := PersonByIdentity(ctx, st, "linear_owner_label", "Theo")
	if err != nil || p.Meta.SourceID != "theo" {
		t.Fatalf("PersonByIdentity(Theo) = %+v, %v", p, err)
	}
	_, err = PersonByIdentity(ctx, st, "linear_owner_label", "NoSuchOwner")
	if err != ErrNoSuchIdentity {
		t.Fatalf("PersonByIdentity(miss) err = %v, want ErrNoSuchIdentity", err)
	}
}
