package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func createTestIdea(t *testing.T, s *Store) Idea {
	t.Helper()
	idea, err := s.CreateIdea(context.Background(), Idea{Title: "test idea", Stage: "raw"})
	if err != nil {
		t.Fatal(err)
	}
	return idea
}

func TestCreateResearchRunRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	idea := createTestIdea(t, s)

	run, err := s.CreateResearchRun(ctx, ResearchRun{IdeaID: idea.ID, Topic: "clinical dictation", Status: "queued", Provenance: "demo_seed"})
	if err != nil {
		t.Fatal(err)
	}
	if run.ID == "" {
		t.Fatal("expected a generated ID")
	}
	if run.CreatedAt.IsZero() {
		t.Fatal("CreatedAt should default to now")
	}
	if !run.Untrusted {
		t.Fatal("Untrusted should always be true")
	}

	got, err := s.GetResearchRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Topic != "clinical dictation" || got.Status != "queued" || got.Provenance != "demo_seed" {
		t.Fatalf("got = %+v", got)
	}
	if !got.Untrusted {
		t.Fatal("Untrusted should read back true")
	}
	if got.AttachedCardID != "" {
		t.Fatalf("AttachedCardID = %q, want empty for a fresh run", got.AttachedCardID)
	}
	if !got.FinishedAt.IsZero() {
		t.Fatalf("FinishedAt = %v, want zero for a fresh run", got.FinishedAt)
	}
}

// TestCreateResearchRunUntrustedAlwaysTrue confirms untrusted is pinned to
// true even when a caller explicitly sets it to false: this is a fixed
// property of every row this table holds, not a per-row choice.
func TestCreateResearchRunUntrustedAlwaysTrue(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	idea := createTestIdea(t, s)

	run, err := s.CreateResearchRun(ctx, ResearchRun{IdeaID: idea.ID, Topic: "t", Status: "queued", Untrusted: false})
	if err != nil {
		t.Fatal(err)
	}
	if !run.Untrusted {
		t.Fatal("Untrusted should be forced true regardless of the caller's value")
	}
	got, err := s.GetResearchRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Untrusted {
		t.Fatal("Untrusted should read back true from the database")
	}
}

func TestCreateResearchRunRejectsUnknownStatus(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	idea := createTestIdea(t, s)
	if _, err := s.CreateResearchRun(ctx, ResearchRun{IdeaID: idea.ID, Topic: "t", Status: "done"}); err == nil {
		t.Fatal("expected an error for an unknown status, got nil")
	}
	all, err := s.ListResearchRuns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("a rejected run should not persist, got %+v", all)
	}
}

func TestCreateResearchRunRejectsUnknownIdea(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if _, err := s.CreateResearchRun(ctx, ResearchRun{IdeaID: "no-such-idea", Topic: "t", Status: "queued"}); err == nil {
		t.Fatal("expected a foreign-key error for a nonexistent idea, got nil")
	}
}

func TestGetResearchRunUnsetReturnsErrNotFound(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if _, err := s.GetResearchRun(ctx, "never-set"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetResearchRun(unset) = %v, want ErrNotFound", err)
	}
}

func TestUpdateResearchRunStatusValidatesAndSetsFinishedAt(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	idea := createTestIdea(t, s)
	run, err := s.CreateResearchRun(ctx, ResearchRun{IdeaID: idea.ID, Topic: "t", Status: "queued"})
	if err != nil {
		t.Fatal(err)
	}

	if err := s.UpdateResearchRunStatus(ctx, run.ID, "running", time.Time{}); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetResearchRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "running" || !got.FinishedAt.IsZero() {
		t.Fatalf("after running: %+v", got)
	}

	finishedAt := ts(5)
	if err := s.UpdateResearchRunStatus(ctx, run.ID, "finished", finishedAt); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetResearchRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "finished" || !got.FinishedAt.Equal(finishedAt) {
		t.Fatalf("after finished: %+v, want FinishedAt = %v", got, finishedAt)
	}

	if err := s.UpdateResearchRunStatus(ctx, run.ID, "bogus", time.Time{}); err == nil {
		t.Fatal("expected an error for an unknown status, got nil")
	}
}

func TestSetResearchRunReportAndAttachedCard(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	idea := createTestIdea(t, s)
	run, err := s.CreateResearchRun(ctx, ResearchRun{IdeaID: idea.ID, Topic: "t", Status: "finished"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetResearchRunReport(ctx, run.ID, "the report says X"); err != nil {
		t.Fatal(err)
	}
	if err := s.AttachResearchRunCard(ctx, run.ID, "card-1"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetResearchRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ReportText != "the report says X" || got.AttachedCardID != "card-1" {
		t.Fatalf("got = %+v", got)
	}
}

func TestListResearchRunsForIdea(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	a := createTestIdea(t, s)
	b, err := s.CreateIdea(ctx, Idea{Title: "b", Stage: "raw"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateResearchRun(ctx, ResearchRun{IdeaID: a.ID, Topic: "t1", Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateResearchRun(ctx, ResearchRun{IdeaID: b.ID, Topic: "t2", Status: "queued"}); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListResearchRunsForIdea(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].IdeaID != a.ID {
		t.Fatalf("ListResearchRunsForIdea(a) = %+v, want only a's run", list)
	}
}

func TestUpsertResearchStepOrderingAndUpdate(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	idea := createTestIdea(t, s)
	run, err := s.CreateResearchRun(ctx, ResearchRun{IdeaID: idea.ID, Topic: "t", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}

	steps := []struct {
		n     int
		label string
	}{
		{1, "overview"}, {2, "market"}, {3, "competitors"}, {4, "pricing"}, {5, "risks"},
	}
	// Insert out of order to prove ListResearchSteps sorts by n, not
	// insertion order.
	for _, want := range []int{3, 1, 5, 2, 4} {
		for _, st := range steps {
			if st.n == want {
				if err := s.UpsertResearchStep(ctx, ResearchStep{RunID: run.ID, N: st.n, Label: st.label, Status: "pending"}); err != nil {
					t.Fatal(err)
				}
			}
		}
	}

	list, err := s.ListResearchSteps(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 5 {
		t.Fatalf("ListResearchSteps = %d steps, want 5", len(list))
	}
	for i, st := range list {
		wantN := i + 1
		if st.N != wantN || st.Label != steps[i].label {
			t.Fatalf("step[%d] = %+v, want n=%d label=%q", i, st, wantN, steps[i].label)
		}
	}

	// Re-upsert step 3 to "done" with a source count; it should update in
	// place, not duplicate.
	if err := s.UpsertResearchStep(ctx, ResearchStep{RunID: run.ID, N: 3, Label: "competitors", Status: "done", SourceCount: 4}); err != nil {
		t.Fatal(err)
	}
	list, err = s.ListResearchSteps(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 5 {
		t.Fatalf("after re-upsert: %d steps, want 5 (no duplicate)", len(list))
	}
	if list[2].Status != "done" || list[2].SourceCount != 4 {
		t.Fatalf("step 3 = %+v, want status=done source_count=4", list[2])
	}
}

func TestUpsertResearchStepRejectsUnknownStatusAndBadN(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	idea := createTestIdea(t, s)
	run, err := s.CreateResearchRun(ctx, ResearchRun{IdeaID: idea.ID, Topic: "t", Status: "running"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertResearchStep(ctx, ResearchStep{RunID: run.ID, N: 1, Label: "overview", Status: "bogus"}); err == nil {
		t.Fatal("expected an error for an unknown status, got nil")
	}
	if err := s.UpsertResearchStep(ctx, ResearchStep{RunID: run.ID, N: 0, Label: "overview", Status: "pending"}); err == nil {
		t.Fatal("expected an error for n < 1, got nil")
	}
	list, err := s.ListResearchSteps(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("no step should have persisted, got %+v", list)
	}
}

func TestUpsertResearchStepRejectsUnknownRun(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if err := s.UpsertResearchStep(ctx, ResearchStep{RunID: "no-such-run", N: 1, Label: "overview", Status: "pending"}); err == nil {
		t.Fatal("expected a foreign-key error for a nonexistent run, got nil")
	}
}
