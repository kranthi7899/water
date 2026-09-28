package store

import (
	"context"
	"errors"
	"sync"
	"testing"
)

func TestCreateThreadGeneratesIDAndTimestamps(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	got, err := s.CreateThread(ctx, Thread{Title: "General chat"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID == "" {
		t.Fatal("CreateThread: ID is empty, want a generated id")
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("CreateThread: timestamps not set, got %+v", got)
	}
}

func TestGetOrCreateThreadForAnchorReturnsSameThreadTwice(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	first, created, err := s.GetOrCreateThreadForAnchor(ctx, "decision", "d-1", "Budget decision", `{"kind":"decision"}`, false)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first GetOrCreateThreadForAnchor: created = false, want true")
	}

	second, created, err := s.GetOrCreateThreadForAnchor(ctx, "decision", "d-1", "ignored title", "ignored context", false)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("second GetOrCreateThreadForAnchor for the same anchor: created = true, want false")
	}
	if second.ID != first.ID {
		t.Fatalf("second call returned thread %q, want the same thread %q", second.ID, first.ID)
	}
}

func TestGetThreadUnknownIDReturnsErrNotFound(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	_, err := s.GetThread(ctx, "thr_does_not_exist")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetThread(unknown) = %v, want ErrNotFound", err)
	}
}

func TestListThreadsOrdersNewestUpdatedFirst(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	a, err := s.CreateThread(ctx, Thread{Title: "older"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateThread(ctx, Thread{Title: "newer"})
	if err != nil {
		t.Fatal(err)
	}
	// Touch a's updated_at after b was created, so a should now sort first.
	if _, err := s.AppendThreadMessage(ctx, ThreadMessage{ThreadID: a.ID, Role: "ceo", Text: "hi"}); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListThreads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].ID != a.ID || all[1].ID != b.ID {
		t.Fatalf("ListThreads = %+v, want [a, b] (a touched most recently)", all)
	}
}

func TestAppendThreadMessageThenThreadMessagesReturnsCreationOrder(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	thread, err := s.CreateThread(ctx, Thread{Title: "conversation"})
	if err != nil {
		t.Fatal(err)
	}

	texts := []string{"first", "second", "third"}
	for _, text := range texts {
		role := "ceo"
		if text == "second" {
			role = "twin"
		}
		if _, err := s.AppendThreadMessage(ctx, ThreadMessage{ThreadID: thread.ID, Role: role, Text: text}); err != nil {
			t.Fatal(err)
		}
	}

	msgs, err := s.ThreadMessages(ctx, thread.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("ThreadMessages = %+v, want 3", msgs)
	}
	for i, want := range texts {
		if msgs[i].Text != want {
			t.Fatalf("ThreadMessages[%d].Text = %q, want %q (creation order)", i, msgs[i].Text, want)
		}
	}
}

func TestGetOrCreateThreadForAnchorConcurrentCallsCreateExactlyOneThread(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	const n = 20
	ids := make([]string, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			th, _, err := s.GetOrCreateThreadForAnchor(ctx, "meeting", "m-race", "Recap", "{}", false)
			if err != nil {
				t.Error(err)
				return
			}
			ids[i] = th.ID
		}(i)
	}
	wg.Wait()

	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" {
			t.Fatal("a concurrent GetOrCreateThreadForAnchor call returned an empty id")
		}
		seen[id] = true
	}
	if len(seen) != 1 {
		t.Fatalf("concurrent calls returned %d distinct thread ids, want exactly 1: %v", len(seen), seen)
	}

	all, err := s.ListThreads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("ListThreads after concurrent GetOrCreateThreadForAnchor = %+v, want exactly 1 thread", all)
	}
}
