package store

import (
	"context"
	"testing"
)

func TestAddCardEvidenceExtraRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	got, err := s.AddCardEvidenceExtra(ctx, CardEvidenceExtra{CardID: "card-1", Source: "research:run-1", Text: "attached report says X", Untrusted: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == 0 {
		t.Fatal("expected a generated ID")
	}
	if got.AddedAt.IsZero() {
		t.Fatal("AddedAt should default to now")
	}

	list, err := s.ListCardEvidenceExtra(ctx, "card-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Text != "attached report says X" || !list[0].Untrusted {
		t.Fatalf("ListCardEvidenceExtra = %+v", list)
	}
}

func TestListCardEvidenceExtraOnlyCardsOwnRows(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if _, err := s.AddCardEvidenceExtra(ctx, CardEvidenceExtra{CardID: "card-a", Source: "s1", Text: "t1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddCardEvidenceExtra(ctx, CardEvidenceExtra{CardID: "card-b", Source: "s2", Text: "t2"}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListCardEvidenceExtra(ctx, "card-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].CardID != "card-a" {
		t.Fatalf("ListCardEvidenceExtra(card-a) = %+v, want only card-a's row", list)
	}
}

func TestAllCardEvidenceExtraGroupsByCardID(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if _, err := s.AddCardEvidenceExtra(ctx, CardEvidenceExtra{CardID: "card-a", Source: "s1", Text: "t1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddCardEvidenceExtra(ctx, CardEvidenceExtra{CardID: "card-a", Source: "s2", Text: "t2", Untrusted: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddCardEvidenceExtra(ctx, CardEvidenceExtra{CardID: "card-b", Source: "s3", Text: "t3"}); err != nil {
		t.Fatal(err)
	}
	all, err := s.AllCardEvidenceExtra(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || len(all["card-a"]) != 2 || len(all["card-b"]) != 1 {
		t.Fatalf("AllCardEvidenceExtra = %+v", all)
	}
	if !all["card-a"][1].Untrusted {
		t.Fatalf("expected card-a's second row to be untrusted: %+v", all["card-a"])
	}
}
