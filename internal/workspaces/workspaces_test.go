package workspaces

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"water"
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

const validSpecYAML = `
id: water
name: Water
template: project
source: linear_team:WAT
teams: [WAT]
`

func TestParseSpecValid(t *testing.T) {
	s, err := ParseSpec([]byte(validSpecYAML))
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "water" || s.Template != "project" || s.Source != "linear_team:WAT" {
		t.Fatalf("spec = %+v", s)
	}
	if s.SpecHash == "" {
		t.Fatal("SpecHash was not filled in")
	}
	if len(s.Teams) != 1 || s.Teams[0] != "WAT" {
		t.Fatalf("Teams = %+v", s.Teams)
	}
}

func TestParseSpecRejectsBadID(t *testing.T) {
	bad := strings.Replace(validSpecYAML, "id: water", "id: Water_NOT_OK!", 1)
	if _, err := ParseSpec([]byte(bad)); err == nil {
		t.Fatal("expected an error for a bad id, got nil")
	}
}

func TestParseSpecRejectsBadTemplate(t *testing.T) {
	bad := strings.Replace(validSpecYAML, "template: project", "template: not_a_template", 1)
	if _, err := ParseSpec([]byte(bad)); err == nil {
		t.Fatal("expected an error for a bad template, got nil")
	}
}

func TestParseSpecRejectsBadSource(t *testing.T) {
	bad := strings.Replace(validSpecYAML, "source: linear_team:WAT", "source: not_a_source", 1)
	if _, err := ParseSpec([]byte(bad)); err == nil {
		t.Fatal("expected an error for a bad source, got nil")
	}
}

func TestParseSpecRejectsUnknownField(t *testing.T) {
	bad := validSpecYAML + "\nbogus_field: true\n"
	if _, err := ParseSpec([]byte(bad)); err == nil {
		t.Fatal("expected an error for an unknown field, got nil")
	}
}

func TestClientsRuleAllAndList(t *testing.T) {
	allYAML := `
id: clients
name: Clients
template: clients
source: company_customers
clients: all
`
	s, err := ParseSpec([]byte(allYAML))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Clients.All {
		t.Fatal("expected Clients.All = true")
	}
	if !s.Rules().Matches(Candidate{ClientID: "anything"}) {
		t.Fatal("clients: all must match any client id")
	}

	listYAML := `
id: clients2
name: Clients2
template: clients
source: company_customers
clients: [acme, globex]
`
	s2, err := ParseSpec([]byte(listYAML))
	if err != nil {
		t.Fatal(err)
	}
	if s2.Clients.All {
		t.Fatal("expected Clients.All = false for a list")
	}
	if !s2.Rules().Matches(Candidate{ClientID: "acme"}) {
		t.Fatal("expected acme to match")
	}
	if s2.Rules().Matches(Candidate{ClientID: "not-listed"}) {
		t.Fatal("expected not-listed to not match")
	}
}

func TestClientsRuleRejectsBadScalar(t *testing.T) {
	bad := `
id: clients3
name: Clients3
template: clients
source: company_customers
clients: some_typo
`
	if _, err := ParseSpec([]byte(bad)); err == nil {
		t.Fatal("expected an error for clients: some_typo, got nil")
	}
}

func TestMembershipRulesMatchRoundTrip(t *testing.T) {
	s, err := ParseSpec([]byte(validSpecYAML))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Rules().Matches(Candidate{TeamKey: "WAT"}) {
		t.Fatal("expected TeamKey WAT to match")
	}
	if s.Rules().Matches(Candidate{TeamKey: "KEV"}) {
		t.Fatal("expected TeamKey KEV to not match")
	}
	if s.Rules().Matches(Candidate{}) {
		t.Fatal("expected a zero-value Candidate to never match")
	}
}

func TestLoadRegistryMissingDirIsEmpty(t *testing.T) {
	r, err := LoadRegistry(fstest.MapFS{}, "nobody")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Specs()) != 0 {
		t.Fatalf("Specs = %+v, want none", r.Specs())
	}
}

func TestLoadRegistryRejectsBadFile(t *testing.T) {
	fsys := fstest.MapFS{
		"twins/t/workspaces/broken.yaml": &fstest.MapFile{Data: []byte("id: [not, a, string]\n")},
	}
	if _, err := LoadRegistry(fsys, "t"); err == nil {
		t.Fatal("expected an error for a malformed workspace file, got nil")
	}
}

func TestLoadRegistryRejectsDuplicateID(t *testing.T) {
	fsys := fstest.MapFS{
		"twins/t/workspaces/a.yaml": &fstest.MapFile{Data: []byte(validSpecYAML)},
		"twins/t/workspaces/b.yaml": &fstest.MapFile{Data: []byte(validSpecYAML)},
	}
	if _, err := LoadRegistry(fsys, "t"); err == nil {
		t.Fatal("expected an error for a duplicate id across two files, got nil")
	}
}

func TestLoadRegistryLoadsAllTwelveRealFiles(t *testing.T) {
	r, err := LoadRegistry(water.TwinsFS(), "ceo")
	if err != nil {
		t.Fatal(err)
	}
	specs := r.Specs()
	if len(specs) != 12 {
		names := make([]string, len(specs))
		for i, s := range specs {
			names[i] = s.ID
		}
		t.Fatalf("got %d specs, want 12: %v", len(specs), names)
	}
	wantIDs := []string{
		"water", "kevin", "yt-recamendo", "crawler", "voice-text", "econ-rag",
		"finance", "clients", "people", "ideas", "research", "marketing",
	}
	for _, id := range wantIDs {
		if _, ok := r.Spec(id); !ok {
			t.Errorf("missing expected workspace %q", id)
		}
	}
}

func TestRegistryMatchingSpecsMultiMatch(t *testing.T) {
	fsys := fstest.MapFS{
		"twins/t/workspaces/proj.yaml": &fstest.MapFile{Data: []byte(`
id: proj
name: Proj
template: project
source: linear_team:WAT
teams: [WAT]
`)},
		"twins/t/workspaces/dom.yaml": &fstest.MapFile{Data: []byte(`
id: dom
name: Dom
template: clients
source: company_customers
clients: all
`)},
	}
	r, err := LoadRegistry(fsys, "t")
	if err != nil {
		t.Fatal(err)
	}
	matches := r.MatchingSpecs(Candidate{TeamKey: "WAT"})
	if len(matches) != 1 || matches[0].ID != "proj" {
		t.Fatalf("TeamKey WAT matches = %+v", matches)
	}
	matches = r.MatchingSpecs(Candidate{ClientID: "acme"})
	if len(matches) != 1 || matches[0].ID != "dom" {
		t.Fatalf("ClientID acme matches = %+v", matches)
	}
	matches = r.MatchingSpecs(Candidate{})
	if len(matches) != 0 {
		t.Fatalf("empty Candidate matches = %+v, want none", matches)
	}
}

func TestRegistrySyncUpsertsAndRecomputesHashOnChange(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	fsys := fstest.MapFS{
		"twins/t/workspaces/proj.yaml": &fstest.MapFile{Data: []byte(validSpecYAML)},
	}
	r, err := LoadRegistry(fsys, "t")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Sync(ctx, st); err != nil {
		t.Fatal(err)
	}
	row, err := store.Get[store.Workspace](ctx, st, SpecSource, "water")
	if err != nil {
		t.Fatal(err)
	}
	if row.Template != "project" || row.PrimarySource != "linear_team:WAT" || row.SpecHash == "" {
		t.Fatalf("row after first sync = %+v", row)
	}
	firstHash := row.SpecHash

	// Changing the file's content and reloading must produce a different
	// spec_hash on re-sync (idempotent overwrite, not a duplicate row).
	fsys["twins/t/workspaces/proj.yaml"] = &fstest.MapFile{Data: []byte(validSpecYAML + "\ndescription: now with a description\n")}
	r2, err := LoadRegistry(fsys, "t")
	if err != nil {
		t.Fatal(err)
	}
	if err := r2.Sync(ctx, st); err != nil {
		t.Fatal(err)
	}
	row2, err := store.Get[store.Workspace](ctx, st, SpecSource, "water")
	if err != nil {
		t.Fatal(err)
	}
	if row2.SpecHash == firstHash {
		t.Fatal("expected spec_hash to change after the file's content changed")
	}
	all, err := store.List[store.Workspace](ctx, st, store.Query{Source: SpecSource})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d rows after re-sync, want exactly 1 (no duplicate)", len(all))
	}
}
