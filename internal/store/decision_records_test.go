package store

import (
	"context"
	"errors"
	"testing"
)

func TestUpsertDecisionRecordRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	r := DecisionRecord{CardID: "card-1", CardJSON: `{"ID":"card-1","Lead":"seeded"}`, Provenance: "demo_seed", Simulated: true}
	if err := s.UpsertDecisionRecord(ctx, r); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDecisionRecord(ctx, "card-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.CardJSON != r.CardJSON || got.Provenance != "demo_seed" || !got.Simulated {
		t.Fatalf("got = %+v", got)
	}
	if got.CreatedAt.IsZero() {
		t.Fatal("CreatedAt should default to now")
	}
}

func TestUpsertDecisionRecordTwiceReplacesInPlace(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	if err := s.UpsertDecisionRecord(ctx, DecisionRecord{CardID: "card-2", CardJSON: `{"Lead":"v1"}`}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertDecisionRecord(ctx, DecisionRecord{CardID: "card-2", CardJSON: `{"Lead":"v2"}`, Provenance: "demo_seed"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetDecisionRecord(ctx, "card-2")
	if err != nil {
		t.Fatal(err)
	}
	if got.CardJSON != `{"Lead":"v2"}` || got.Provenance != "demo_seed" {
		t.Fatalf("got = %+v, want the second upsert's values with no duplicate row", got)
	}
	all, err := s.ListDecisionRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("ListDecisionRecords = %d rows, want 1", len(all))
	}
}

func TestGetDecisionRecordUnsetReturnsErrNotFound(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if _, err := s.GetDecisionRecord(ctx, "never-set"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetDecisionRecord(unset) = %v, want ErrNotFound", err)
	}
}

func TestDeleteDecisionRecord(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.UpsertDecisionRecord(ctx, DecisionRecord{CardID: "card-3", CardJSON: `{}`}); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteDecisionRecord(ctx, "card-3"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetDecisionRecord(ctx, "card-3"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v, want ErrNotFound", err)
	}
	// Deleting a row that never existed is a no-op, not an error.
	if err := s.DeleteDecisionRecord(ctx, "never-existed"); err != nil {
		t.Fatal(err)
	}
}

func TestListDecisionRecordsOrdersByCreatedAt(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.UpsertDecisionRecord(ctx, DecisionRecord{CardID: "card-b", CardJSON: `{}`, CreatedAt: ts(2)}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertDecisionRecord(ctx, DecisionRecord{CardID: "card-a", CardJSON: `{}`, CreatedAt: ts(1)}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListDecisionRecords(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].CardID != "card-a" || all[1].CardID != "card-b" {
		t.Fatalf("ListDecisionRecords = %+v, want card-a then card-b", all)
	}
}
