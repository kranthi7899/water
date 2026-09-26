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

// TestRepointCardStateMovesOnlyStagedCardsOnTheOldEnvelope: an approval
// edit re-points the staged card from the voided envelope to the new one;
// a dismissed card that happens to name the same envelope, and a staged
// card on another envelope, are left alone.
func TestRepointCardStateMovesOnlyStagedCardsOnTheOldEnvelope(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	for _, cs := range []CardState{
		{CardID: "card-a", Status: "staged", ApprovalID: "env_old"},
		{CardID: "card-b", Status: "dismissed", ApprovalID: "env_old"},
		{CardID: "card-c", Status: "staged", ApprovalID: "env_other"},
	} {
		if err := s.SetCardState(ctx, cs); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.RepointCardState(ctx, "env_old", "env_new")
	if err != nil || n != 1 {
		t.Fatalf("RepointCardState = %d, %v; want 1 row", n, err)
	}
	want := map[string]string{"card-a": "env_new", "card-b": "env_old", "card-c": "env_other"}
	for id, approval := range want {
		got, err := s.GetCardState(ctx, id)
		if err != nil || got.ApprovalID != approval {
			t.Errorf("%s: approval_id = %q (%v), want %q", id, got.ApprovalID, err, approval)
		}
	}
	if n, err := s.RepointCardState(ctx, "env_nobody", "env_x"); err != nil || n != 0 {
		t.Fatalf("unknown old id: %d, %v; want 0", n, err)
	}
	if n, err := s.RepointCardState(ctx, "", "env_x"); err != nil || n != 0 {
		t.Fatalf("empty old id: %d, %v; want 0", n, err)
	}

	staged, err := s.StagedCardStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 2 || staged["card-a"].ApprovalID != "env_new" || staged["card-c"].ApprovalID != "env_other" {
		t.Fatalf("StagedCardStates = %+v, want card-a and card-c only", staged)
	}
}
