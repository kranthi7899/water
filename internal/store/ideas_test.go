package store

import (
	"context"
	"errors"
	"testing"
)

func TestCreateIdeaRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	idea, err := s.CreateIdea(ctx, Idea{Title: "On-device short-utterance mode", Gist: "cheap, no network", Stage: "raw", Provenance: "demo_seed", Simulated: true})
	if err != nil {
		t.Fatal(err)
	}
	if idea.ID == "" {
		t.Fatal("expected a generated ID")
	}
	if idea.CreatedAt.IsZero() {
		t.Fatal("CreatedAt should default to now")
	}

	got, err := s.GetIdea(ctx, idea.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != idea.Title || got.Stage != "raw" || got.Provenance != "demo_seed" || !got.Simulated {
		t.Fatalf("got = %+v", got)
	}
}

func TestCreateIdeaRejectsUnknownStage(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if _, err := s.CreateIdea(ctx, Idea{Title: "x", Stage: "shipped"}); err == nil {
		t.Fatal("expected an error for an unknown stage, got nil")
	}
	all, err := s.ListIdeas(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("a rejected idea should not persist, got %+v", all)
	}
}

func TestSetIdeaStageValidatesAndUpdates(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	idea, err := s.CreateIdea(ctx, Idea{Title: "x", Stage: "raw"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetIdeaStage(ctx, idea.ID, "explored"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetIdea(ctx, idea.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != "explored" {
		t.Fatalf("Stage = %q, want explored", got.Stage)
	}
	if err := s.SetIdeaStage(ctx, idea.ID, "shipped"); err == nil {
		t.Fatal("expected an error for an unknown stage, got nil")
	}
	got, err = s.GetIdea(ctx, idea.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Stage != "explored" {
		t.Fatalf("a rejected SetIdeaStage should not change the row, got Stage = %q", got.Stage)
	}
}

func TestGetIdeaUnsetReturnsErrNotFound(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if _, err := s.GetIdea(ctx, "never-set"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetIdea(unset) = %v, want ErrNotFound", err)
	}
}

func TestListIdeasOrdersByCreatedAt(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if _, err := s.CreateIdea(ctx, Idea{ID: "idea-b", Title: "b", Stage: "raw", CreatedAt: ts(2)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateIdea(ctx, Idea{ID: "idea-a", Title: "a", Stage: "raw", CreatedAt: ts(1)}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListIdeas(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != "idea-a" || all[1].ID != "idea-b" {
		t.Fatalf("ListIdeas = %+v, want idea-a then idea-b", all)
	}
}

func TestAddIdeaEvidenceRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	idea, err := s.CreateIdea(ctx, Idea{Title: "x", Stage: "raw"})
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.AddIdeaEvidence(ctx, IdeaEvidence{IdeaID: idea.ID, Source: "web:example.com/article", Label: "market is growing"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == 0 {
		t.Fatal("expected a generated ID")
	}
	if got.AddedAt.IsZero() {
		t.Fatal("AddedAt should default to now")
	}

	list, err := s.ListIdeaEvidence(ctx, idea.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Label != "market is growing" {
		t.Fatalf("ListIdeaEvidence = %+v", list)
	}
}

// TestAddIdeaEvidenceRejectsUnknownIdea documents the chosen behaviour for
// evidence attached to a nonexistent idea: reject it (a foreign-key
// constraint error), not silently orphan it. Every idea this table can
// reference is a real row in the ideas table one insert away (unlike a
// decision card, which may exist only computed), so there's no legitimate
// "orphaned evidence" case to support.
func TestAddIdeaEvidenceRejectsUnknownIdea(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if _, err := s.AddIdeaEvidence(ctx, IdeaEvidence{IdeaID: "no-such-idea", Source: "s", Label: "l"}); err == nil {
		t.Fatal("expected a foreign-key error for a nonexistent idea, got nil")
	}
}

func TestListIdeaEvidenceOnlyOwnRows(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	a, err := s.CreateIdea(ctx, Idea{Title: "a", Stage: "raw"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateIdea(ctx, Idea{Title: "b", Stage: "raw"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddIdeaEvidence(ctx, IdeaEvidence{IdeaID: a.ID, Source: "s1", Label: "l1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddIdeaEvidence(ctx, IdeaEvidence{IdeaID: b.ID, Source: "s2", Label: "l2"}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListIdeaEvidence(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].IdeaID != a.ID {
		t.Fatalf("ListIdeaEvidence(a) = %+v, want only a's row", list)
	}
}
