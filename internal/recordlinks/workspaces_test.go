package recordlinks

import (
	"context"
	"testing"
	"testing/fstest"

	"water/internal/roster"
	"water/internal/store"
	"water/internal/workspaces"
)

// rosterFixtureYAML mirrors internal/roster's own minimalYAML fixture
// (roster_test.go): team "crawler" (linear_key CRA) has one project,
// "ingestion-v2", and one client, "atlas-research", whose product names
// the same team.
const rosterFixtureYAML = `
company: {name: Test Co, headcount: 3, blended_hourly_cost_usd: 80}

teams:
  - {id: crawler, name: Crawler, linear_key: CRA}
  - {id: lonely,  name: Lonely,  linear_key: LON}

projects:
  - {id: ingestion-v2, name: Ingestion v2, team: crawler, linear: P-CRA-1, start: 2026-08-18, target: 2026-11-15}

people:
  - id: theo
    name: Theo
    role: Engineer
    home_team: crawler
    allocation: {ingestion-v2: 1.0}
    identities: {linear_owner_label: Theo}

external:
  clients:
    - {id: atlas-research, name: Atlas Research, product: crawler, owner: theo, mrr_usd: 4000}
`

func loadRosterFixture(t *testing.T, st *store.Store) {
	t.Helper()
	fsys := fstest.MapFS{
		"twins/ceo/seed/people.yaml": &fstest.MapFile{Data: []byte(rosterFixtureYAML)},
	}
	if err := roster.Load(context.Background(), fsys, "ceo", st); err != nil {
		t.Fatal(err)
	}
}

func TestIdentifierTeamKey(t *testing.T) {
	cases := map[string]string{
		"WAT-123 Fix the thing":      "WAT",
		"cra-9 lowercase identifier": "CRA",
		"no identifier here":         "",
		"":                           "",
	}
	for title, want := range cases {
		if got := IdentifierTeamKey(title); got != want {
			t.Errorf("IdentifierTeamKey(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestForProjectFromIssue(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	loadRosterFixture(t, st)

	issue := store.Issue{Meta: store.Meta{Source: "linear", SourceID: "issue-1"}, Title: "CRA-42 Crawl faster"}
	if err := ForProjectFromIssue(ctx, st, issue); err != nil {
		t.Fatal(err)
	}
	links, err := st.LinksFrom(ctx, IssueType, "issue-1", store.LinkForProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].ToID != "ingestion-v2" {
		t.Fatalf("for_project links = %+v", links)
	}

	// Re-running over the same issue must not duplicate the edge.
	if err := ForProjectFromIssue(ctx, st, issue); err != nil {
		t.Fatal(err)
	}
	links, err = st.LinksFrom(ctx, IssueType, "issue-1", store.LinkForProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("after re-run, for_project links = %+v, want exactly 1 (no duplicate)", links)
	}
}

func TestForProjectFromIssueNoMatchWritesNothing(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	loadRosterFixture(t, st)

	// An unrecognized prefix.
	if err := ForProjectFromIssue(ctx, st, store.Issue{Meta: store.Meta{Source: "linear", SourceID: "issue-2"}, Title: "ZZZ-1 Unknown team"}); err != nil {
		t.Fatal(err)
	}
	links, err := st.LinksFrom(ctx, IssueType, "issue-2", store.LinkForProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("links = %+v, want none for an unrecognized prefix", links)
	}

	// A known team (LON) with no projects.
	if err := ForProjectFromIssue(ctx, st, store.Issue{Meta: store.Meta{Source: "linear", SourceID: "issue-3"}, Title: "LON-1 Lonely team"}); err != nil {
		t.Fatal(err)
	}
	links, err = st.LinksFrom(ctx, IssueType, "issue-3", store.LinkForProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("links = %+v, want none for a team with no projects", links)
	}
}

func TestForProjectFromClient(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	loadRosterFixture(t, st)

	client, err := store.Get[store.Client](ctx, st, "seed", "atlas-research")
	if err != nil {
		t.Fatal(err)
	}
	if err := ForProjectFromClient(ctx, st, *client); err != nil {
		t.Fatal(err)
	}
	links, err := st.LinksFrom(ctx, ClientType, "atlas-research", store.LinkForProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].ToID != "ingestion-v2" {
		t.Fatalf("for_project links = %+v", links)
	}

	if err := ForProjectFromClient(ctx, st, *client); err != nil {
		t.Fatal(err)
	}
	links, err = st.LinksFrom(ctx, ClientType, "atlas-research", store.LinkForProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("after re-run, for_project links = %+v, want exactly 1 (no duplicate)", links)
	}
}

func testRegistry(t *testing.T) *workspaces.Registry {
	t.Helper()
	fsys := fstest.MapFS{
		"twins/t/workspaces/proj.yaml": &fstest.MapFile{Data: []byte(`
id: proj
name: Proj
template: project
source: linear_team:CRA
teams: [CRA]
`)},
		"twins/t/workspaces/dom.yaml": &fstest.MapFile{Data: []byte(`
id: dom
name: Dom
template: clients
source: company_customers
clients: all
`)},
	}
	r, err := workspaces.LoadRegistry(fsys, "t")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestInWorkspaceMultiMatch(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	reg := testRegistry(t)

	// A client matches only the domain workspace (clients: all).
	if err := InWorkspace(ctx, st, reg, ClientType, "atlas-research", workspaces.Candidate{ClientID: "atlas-research"}); err != nil {
		t.Fatal(err)
	}
	links, err := st.LinksFrom(ctx, ClientType, "atlas-research", store.LinkInWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].ToID != "dom" {
		t.Fatalf("client in_workspace links = %+v", links)
	}

	// An issue with team key CRA matches only the project workspace.
	if err := InWorkspaceForIssue(ctx, st, reg, store.Issue{Meta: store.Meta{SourceID: "issue-1"}, Title: "CRA-1 Something"}); err != nil {
		t.Fatal(err)
	}
	links, err = st.LinksFrom(ctx, IssueType, "issue-1", store.LinkInWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 || links[0].ToID != "proj" {
		t.Fatalf("issue in_workspace links = %+v", links)
	}
}

func TestInWorkspaceNoMatchWritesNothing(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	reg := testRegistry(t)

	if err := InWorkspaceForIssue(ctx, st, reg, store.Issue{Meta: store.Meta{SourceID: "issue-2"}, Title: "ZZZ-1 No such team"}); err != nil {
		t.Fatal(err)
	}
	links, err := st.LinksFrom(ctx, IssueType, "issue-2", store.LinkInWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("links = %+v, want none", links)
	}
}

func TestInWorkspaceNoDuplicateOnRerun(t *testing.T) {
	ctx := context.Background()
	st := newStore(t)
	reg := testRegistry(t)

	client := workspaces.Candidate{ClientID: "atlas-research"}
	for i := 0; i < 2; i++ {
		if err := InWorkspace(ctx, st, reg, ClientType, "atlas-research", client); err != nil {
			t.Fatal(err)
		}
	}
	links, err := st.LinksFrom(ctx, ClientType, "atlas-research", store.LinkInWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("links after 2 runs = %+v, want exactly 1 (no duplicate)", links)
	}
}
