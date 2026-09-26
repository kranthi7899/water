package store

import (
	"context"
	"errors"
	"testing"
)

func TestSetCardStateAndGetRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	cs := CardState{CardID: "card-1", Status: "dismissed", Reason: "not relevant", ApprovalID: ""}
	if err := s.SetCardState(ctx, cs); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetCardState(ctx, "card-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "dismissed" || got.Reason != "not relevant" {
		t.Fatalf("got = %+v", got)
	}
}

func TestSetCardStateTwiceUpdatesInPlace(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	if err := s.SetCardState(ctx, CardState{CardID: "card-2", Status: "staged", Reason: "reviewing"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCardState(ctx, CardState{CardID: "card-2", Status: "dismissed", Reason: "changed my mind", ApprovalID: "env_1"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetCardState(ctx, "card-2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "dismissed" || got.Reason != "changed my mind" || got.ApprovalID != "env_1" {
		t.Fatalf("got = %+v, want the second SetCardState's values with no duplicate row", got)
	}

	ids, err := s.DismissedCardIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || !ids["card-2"] {
		t.Fatalf("DismissedCardIDs = %+v, want exactly {card-2: true}", ids)
	}
}

func TestGetCardStateUnsetReturnsErrNotFound(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	_, err := s.GetCardState(ctx, "never-set")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetCardState(unset) = %v, want ErrNotFound", err)
	}
}

func TestDismissedCardIDsExcludesStaged(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	if err := s.SetCardState(ctx, CardState{CardID: "card-dismissed", Status: "dismissed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCardState(ctx, CardState{CardID: "card-staged", Status: "staged"}); err != nil {
		t.Fatal(err)
	}

	ids, err := s.DismissedCardIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || !ids["card-dismissed"] || ids["card-staged"] {
		t.Fatalf("DismissedCardIDs = %+v, want only card-dismissed", ids)
	}
}
