package store

import (
	"context"
	"testing"
)

func TestPersonUpsertGetAndIdentity(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	p := &Person{
		Meta: Meta{Source: "seed", SourceID: "theo", External: false},
		Name: "Theo", Role: "Engineer", HomeTeam: "crawler",
		Identities: `{"linear_owner_label":"Theo"}`,
	}
	if err := s.Upsert(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := Get[Person](ctx, s, "seed", "theo")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Theo" || got.HomeTeam != "crawler" {
		t.Fatalf("got = %+v", got)
	}
	if id := got.Identity("linear_owner_label"); id != "Theo" {
		t.Fatalf("Identity(linear_owner_label) = %q, want Theo", id)
	}
	if id := got.Identity("slack"); id != "" {
		t.Fatalf("Identity(slack) = %q, want empty (absent key)", id)
	}
}

func TestPersonIdentityMalformedJSONReadsAsAbsent(t *testing.T) {
	p := Person{Identities: "not json"}
	if id := p.Identity("linear_owner_label"); id != "" {
		t.Fatalf("Identity on malformed JSON = %q, want empty, not a panic or error", id)
	}
}

func TestTeamProjectClientVendorOrgContactRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	if err := s.Upsert(ctx, &Team{Meta: Meta{Source: "seed", SourceID: "crawler"}, Name: "Crawler", LinearKey: "CRA"}); err != nil {
		t.Fatal(err)
	}
	team, err := Get[Team](ctx, s, "seed", "crawler")
	if err != nil || team.LinearKey != "CRA" {
		t.Fatalf("team = %+v, %v", team, err)
	}

	if err := s.Upsert(ctx, &Project{
		Meta: Meta{Source: "seed", SourceID: "ingestion-v2"}, Name: "Ingestion v2",
		LinearProject: "P-CRA-1", StartAt: ts(0), TargetAt: ts(100),
	}); err != nil {
		t.Fatal(err)
	}
	proj, err := Get[Project](ctx, s, "seed", "ingestion-v2")
	if err != nil || proj.LinearProject != "P-CRA-1" || !proj.StartAt.Equal(ts(0)) {
		t.Fatalf("project = %+v, %v", proj, err)
	}

	if err := s.Upsert(ctx, &Client{
		Meta: Meta{Source: "seed", SourceID: "atlas-research"}, Name: "Atlas Research",
		Product: "crawler", MRRMinor: 400000, Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	cl, err := Get[Client](ctx, s, "seed", "atlas-research")
	if err != nil || cl.MRRMinor != 400000 {
		t.Fatalf("client = %+v, %v", cl, err)
	}

	if err := s.Upsert(ctx, &Vendor{
		Meta: Meta{Source: "seed", SourceID: "lexicon-archive"}, Name: "Lexicon Archive",
		Product: "econ-rag", MonthlyMinor: 280000,
	}); err != nil {
		t.Fatal(err)
	}
	v, err := Get[Vendor](ctx, s, "seed", "lexicon-archive")
	if err != nil || v.MonthlyMinor != 280000 {
		t.Fatalf("vendor = %+v, %v", v, err)
	}

	if err := s.Upsert(ctx, &OrgContact{
		Meta: Meta{Source: "seed", SourceID: "alex"}, Name: "Alex",
		Role: "Candidate", ForTeam: "crawler", Stage: "final_interview",
	}); err != nil {
		t.Fatal(err)
	}
	oc, err := Get[OrgContact](ctx, s, "seed", "alex")
	if err != nil || oc.Stage != "final_interview" {
		t.Fatalf("org contact = %+v, %v", oc, err)
	}
}
