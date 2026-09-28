package store

import (
	"context"
	"testing"
)

func TestWorkspaceUpsertGetAndList(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	ws := &Workspace{
		Meta: Meta{Source: "ui", SourceID: "ws-main"},
		Name: "Main workspace", Description: "Everything the CEO is tracking",
	}
	if err := s.Upsert(ctx, ws); err != nil {
		t.Fatal(err)
	}

	got, err := Get[Workspace](ctx, s, "ui", "ws-main")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Main workspace" || got.Description != "Everything the CEO is tracking" {
		t.Fatalf("got = %+v", got)
	}

	all, err := List[Workspace](ctx, s, Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Name != "Main workspace" {
		t.Fatalf("List = %+v", all)
	}
}

// TestWorkspaceSpecColumnsDefaultAndRoundTrip is migration 0017's own test:
// a pre-0017-style workspace row (Template/PrimarySource/SpecHash all
// unset) reads back at their ” default, and a spec-loaded row
// (internal/workspaces) round-trips all three through Upsert/Get.
func TestWorkspaceSpecColumnsDefaultAndRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	if err := s.Upsert(ctx, &Workspace{
		Meta: Meta{Source: "ui", SourceID: "ws-old"},
		Name: "Old-style workspace",
	}); err != nil {
		t.Fatal(err)
	}
	old, err := Get[Workspace](ctx, s, "ui", "ws-old")
	if err != nil {
		t.Fatal(err)
	}
	if old.Template != "" || old.PrimarySource != "" || old.SpecHash != "" {
		t.Fatalf("pre-0017 row = %+v, want all three spec columns at their '' default", old)
	}

	if err := s.Upsert(ctx, &Workspace{
		Meta:          Meta{Source: "workspace_spec", SourceID: "water"},
		Name:          "Water",
		Template:      "project",
		PrimarySource: "linear_team:WAT",
		SpecHash:      "deadbeef",
	}); err != nil {
		t.Fatal(err)
	}
	got, err := Get[Workspace](ctx, s, "workspace_spec", "water")
	if err != nil {
		t.Fatal(err)
	}
	if got.Template != "project" || got.PrimarySource != "linear_team:WAT" || got.SpecHash != "deadbeef" {
		t.Fatalf("spec-loaded row = %+v", got)
	}
}
