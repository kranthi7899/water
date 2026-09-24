package store

import (
	"context"
	"testing"
)

func TestCursorRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	if _, ok, err := s.GetCursor(ctx, "gmail:history_id"); ok || err != nil {
		t.Fatalf("missing cursor: ok=%v err=%v", ok, err)
	}

	if err := s.SetCursor(ctx, "gmail:history_id", "111"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := s.GetCursor(ctx, "gmail:history_id"); !ok || err != nil || v != "111" {
		t.Fatalf("got %q %v %v, want 111", v, ok, err)
	}

	// Overwrite.
	if err := s.SetCursor(ctx, "gmail:history_id", "222"); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := s.GetCursor(ctx, "gmail:history_id"); !ok || err != nil || v != "222" {
		t.Fatalf("after overwrite: got %q %v %v, want 222", v, ok, err)
	}

	// A distinct key is unaffected.
	if err := s.SetCursor(ctx, "gcal:primary:sync_token", "tok1"); err != nil {
		t.Fatal(err)
	}
	if v, ok, _ := s.GetCursor(ctx, "gmail:history_id"); !ok || v != "222" {
		t.Fatalf("unrelated key changed: %q", v)
	}

	if err := s.DeleteCursor(ctx, "gmail:history_id"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.GetCursor(ctx, "gmail:history_id"); ok || err != nil {
		t.Fatalf("after delete: ok=%v err=%v", ok, err)
	}
	// The other key survives an unrelated delete.
	if v, ok, _ := s.GetCursor(ctx, "gcal:primary:sync_token"); !ok || v != "tok1" {
		t.Fatalf("unrelated key deleted: %q %v", v, ok)
	}

	// Deleting an already-absent key is not an error.
	if err := s.DeleteCursor(ctx, "gmail:history_id"); err != nil {
		t.Fatal(err)
	}
}

func TestBriefRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	if _, ok, err := s.GetBrief(ctx, "2026-09-24"); ok || err != nil {
		t.Fatalf("missing brief: ok=%v err=%v", ok, err)
	}

	if err := s.SetBrief(ctx, "2026-09-24", "3 meetings today."); err != nil {
		t.Fatal(err)
	}
	if text, ok, err := s.GetBrief(ctx, "2026-09-24"); !ok || err != nil || text != "3 meetings today." {
		t.Fatalf("got %q %v %v", text, ok, err)
	}

	// Overwrite same day (e.g. background precompute then a later recompute).
	if err := s.SetBrief(ctx, "2026-09-24", "updated brief."); err != nil {
		t.Fatal(err)
	}
	if text, _, _ := s.GetBrief(ctx, "2026-09-24"); text != "updated brief." {
		t.Fatalf("after overwrite: got %q, want updated brief.", text)
	}

	// A different day is independent.
	if _, ok, err := s.GetBrief(ctx, "2026-09-25"); ok || err != nil {
		t.Fatalf("other day should be empty: ok=%v err=%v", ok, err)
	}
}

// TestCursorAndBriefConcurrentWrites confirms the store's single-connection
// serialization (see Open) means concurrent accessor calls never race or
// deadlock, mirroring the concurrency check in internal/memory's tests.
func TestCursorAndBriefConcurrentWrites(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	done := make(chan struct{})

	for w := 0; w < 2; w++ {
		go func(w int) {
			for i := 0; i < 40; i++ {
				_ = s.SetCursor(ctx, "gmail:history_id", "v")
				_ = s.SetBrief(ctx, "2026-09-24", "b")
				_, _, _ = s.GetCursor(ctx, "gmail:history_id")
				_, _, _ = s.GetBrief(ctx, "2026-09-24")
			}
			done <- struct{}{}
		}(w)
	}
	<-done
	<-done

	if v, ok, err := s.GetCursor(ctx, "gmail:history_id"); !ok || err != nil || v != "v" {
		t.Fatalf("after concurrent writers: %q %v %v", v, ok, err)
	}
	if text, ok, err := s.GetBrief(ctx, "2026-09-24"); !ok || err != nil || text != "b" {
		t.Fatalf("after concurrent writers: %q %v %v", text, ok, err)
	}
}
