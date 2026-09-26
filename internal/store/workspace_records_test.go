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
