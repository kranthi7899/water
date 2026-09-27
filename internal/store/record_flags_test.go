package store

import (
	"context"
	"errors"
	"testing"
)

func TestSetRecordFlagRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	// A fixture message row, never a real one (see the phase's ground
	// rule against touching ~/.water/water.db). meta() is the same
	// helper every other store test uses to build store.Meta.
	if err := s.Upsert(ctx, &Message{Meta: meta("msg-fixture-1", true), Subject: "seeded calendar recap"}); err != nil {
		t.Fatal(err)
	}

	if err := s.SetRecordFlag(ctx, RecordFlag{TableName: "messages", Source: "fake", SourceID: "msg-fixture-1", Provenance: "demo_seed", Simulated: true}); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetRecordFlag(ctx, "messages", "fake", "msg-fixture-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Provenance != "demo_seed" || !got.Simulated {
		t.Fatalf("got = %+v", got)
	}
	if got.FlaggedAt.IsZero() {
		t.Fatal("FlaggedAt should default to now")
	}
}

func TestGetRecordFlagUnflaggedReturnsErrNotFound(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	if err := s.Upsert(ctx, &Message{Meta: meta("msg-fixture-2", true), Subject: "an ordinary, unflagged message"}); err != nil {
		t.Fatal(err)
	}

	if _, err := s.GetRecordFlag(ctx, "messages", "fake", "msg-fixture-2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetRecordFlag(unflagged) = %v, want ErrNotFound", err)
	}
}

func TestSetRecordFlagTwiceReplacesInPlace(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.SetRecordFlag(ctx, RecordFlag{TableName: "events", Source: "fake", SourceID: "evt-1", Provenance: "demo_seed", Simulated: false}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRecordFlag(ctx, RecordFlag{TableName: "events", Source: "fake", SourceID: "evt-1", Provenance: "demo_seed", Simulated: true}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRecordFlag(ctx, "events", "fake", "evt-1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Simulated {
		t.Fatal("second SetRecordFlag should have replaced Simulated in place")
	}
	all, err := s.ListRecordFlags(ctx, "events")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("ListRecordFlags = %d rows, want 1 (no duplicate)", len(all))
	}
}

func TestListRecordFlagsScopesByTableName(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.SetRecordFlag(ctx, RecordFlag{TableName: "messages", Source: "fake", SourceID: "m1", Provenance: "demo_seed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRecordFlag(ctx, RecordFlag{TableName: "events", Source: "fake", SourceID: "e1", Provenance: "demo_seed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRecordFlag(ctx, RecordFlag{TableName: "messages", Source: "fake", SourceID: "m2", Provenance: "demo_seed"}); err != nil {
		t.Fatal(err)
	}
	msgs, err := s.ListRecordFlags(ctx, "messages")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("ListRecordFlags(messages) = %+v, want 2 rows", msgs)
	}
	events, err := s.ListRecordFlags(ctx, "events")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("ListRecordFlags(events) = %+v, want 1 row", events)
	}
}
