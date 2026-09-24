package store

import (
	"context"
	"testing"
)

func TestIntentStateRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	states, err := s.ListIntentStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 0 {
		t.Fatalf("fresh store should have no disabled intents, got %v", states)
	}

	if err := s.SetIntentState(ctx, "learned.foo", true, "auto: miss 30% over 12", ts(10)); err != nil {
		t.Fatal(err)
	}
	states, err = s.ListIntentStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if states["learned.foo"] != "auto: miss 30% over 12" {
		t.Fatalf("ListIntentStates = %v", states)
	}

	// Re-disabling with a new reason overwrites, not duplicates.
	if err := s.SetIntentState(ctx, "learned.foo", true, "manual", ts(11)); err != nil {
		t.Fatal(err)
	}
	states, _ = s.ListIntentStates(ctx)
	if len(states) != 1 || states["learned.foo"] != "manual" {
		t.Fatalf("re-disable should overwrite reason, got %v", states)
	}

	// Re-enabling removes it from the disabled set entirely.
	if err := s.SetIntentState(ctx, "learned.foo", false, "", ts(12)); err != nil {
		t.Fatal(err)
	}
	states, err = s.ListIntentStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 0 {
		t.Fatalf("after enable, ListIntentStates should be empty, got %v", states)
	}
}

func TestIntentStateMultipleIntents(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.SetIntentState(ctx, "learned.a", true, "manual", ts(10)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetIntentState(ctx, "learned.b", true, "auto: miss", ts(10)); err != nil {
		t.Fatal(err)
	}
	states, err := s.ListIntentStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 || states["learned.a"] != "manual" || states["learned.b"] != "auto: miss" {
		t.Fatalf("ListIntentStates = %v", states)
	}
	if err := s.SetIntentState(ctx, "learned.a", false, "", ts(11)); err != nil {
		t.Fatal(err)
	}
	states, _ = s.ListIntentStates(ctx)
	if len(states) != 1 {
		t.Fatalf("expected only learned.b to remain disabled, got %v", states)
	}
	if _, ok := states["learned.a"]; ok {
		t.Fatal("learned.a should have been removed")
	}
}
