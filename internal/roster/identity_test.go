package roster

import (
	"context"
	"testing"
	"testing/fstest"

	"water/internal/store"
)

func TestTeamByLinearKeyResolvesAndMisses(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	fsys := fstest.MapFS{
		"twins/ceo/seed/people.yaml": &fstest.MapFile{Data: []byte(minimalYAML)},
	}
	if err := Load(ctx, fsys, "ceo", st); err != nil {
		t.Fatal(err)
	}
	team, err := TeamByLinearKey(ctx, st, "CRA")
	if err != nil || team.Meta.SourceID != "crawler" {
		t.Fatalf("TeamByLinearKey(CRA) = %+v, %v", team, err)
	}
	// Case-insensitive.
	if team2, err := TeamByLinearKey(ctx, st, "cra"); err != nil || team2.Meta.SourceID != "crawler" {
		t.Fatalf("TeamByLinearKey(cra) = %+v, %v", team2, err)
	}
	if _, err := TeamByLinearKey(ctx, st, "NOPE"); err != ErrNoSuchTeam {
		t.Fatalf("TeamByLinearKey(NOPE) err = %v, want ErrNoSuchTeam", err)
	}
	if _, err := TeamByLinearKey(ctx, st, ""); err != ErrNoSuchTeam {
		t.Fatalf("TeamByLinearKey(\"\") err = %v, want ErrNoSuchTeam", err)
	}
}

func TestProjectsForTeam(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	fsys := fstest.MapFS{
		"twins/ceo/seed/people.yaml": &fstest.MapFile{Data: []byte(minimalYAML)},
	}
	if err := Load(ctx, fsys, "ceo", st); err != nil {
		t.Fatal(err)
	}
	projects, err := ProjectsForTeam(ctx, st, "crawler")
	if err != nil || len(projects) != 1 || projects[0] != "ingestion-v2" {
		t.Fatalf("ProjectsForTeam(crawler) = %+v, %v", projects, err)
	}
	// A team with no projects, and an unknown team, both return empty, not
	// an error.
	if err := st.Upsert(ctx, &store.Team{Meta: store.Meta{Source: "seed", SourceID: "lonely"}, Name: "Lonely", LinearKey: "LON"}); err != nil {
		t.Fatal(err)
	}
	projects, err = ProjectsForTeam(ctx, st, "lonely")
	if err != nil || len(projects) != 0 {
		t.Fatalf("ProjectsForTeam(lonely) = %+v, %v", projects, err)
	}
	projects, err = ProjectsForTeam(ctx, st, "no-such-team")
	if err != nil || len(projects) != 0 {
		t.Fatalf("ProjectsForTeam(no-such-team) = %+v, %v", projects, err)
	}
}
