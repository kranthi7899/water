package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

func TestOpenReadOnlyRejectsWrites(t *testing.T) {
	s, path := openTemp(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, &Message{Meta: meta("m1", true), From: "dana@x.com", SentAt: ts(7)}); err != nil {
		t.Fatal(err)
	}

	ro, err := OpenReadOnly(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()

	// A plain read must work.
	msgs, err := ro.LatestMessages(ctx, 10)
	if err != nil {
		t.Fatalf("read through read-only handle: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}

	// A write attempt must fail at the SQLite level, not silently no-op.
	if _, err := ro.db.ExecContext(ctx, `INSERT INTO messages (source, source_id, external, sender) VALUES ('x','y',0,'z')`); err == nil {
		t.Fatal("INSERT through read-only handle should have failed")
	}
	if _, err := ro.db.ExecContext(ctx, `UPDATE messages SET subject = 'hacked'`); err == nil {
		t.Fatal("UPDATE through read-only handle should have failed")
	}
	if _, err := ro.db.ExecContext(ctx, `DELETE FROM messages`); err == nil {
		t.Fatal("DELETE through read-only handle should have failed")
	}

	// Confirm the write attempts truly didn't mutate anything (belt and
	// braces beyond "returned an error").
	msgs, err = ro.LatestMessages(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Subject == "hacked" {
		t.Fatalf("data mutated despite read-only handle: %+v", msgs)
	}
}

func TestOpenReadOnlyRunsNoMigrations(t *testing.T) {
	// OpenReadOnly against a path that has never been opened by the writer
	// must fail (no file, and it must not create + migrate one), proving it
	// doesn't run the migration path itself.
	dir := t.TempDir()
	path := dir + "/never-created.db"
	ro, err := OpenReadOnly(path, 1)
	if err != nil {
		// Some drivers fail immediately on mode=ro against a missing file;
		// that's an acceptable way to satisfy "runs no migrations."
		return
	}
	defer ro.Close()
	if _, err := ro.db.ExecContext(context.Background(), `SELECT 1 FROM sqlite_master LIMIT 1`); err == nil {
		t.Fatal("expected an error opening a nonexistent database read-only, or a migrated schema_migrations table to be absent")
	}
}

func TestReadPoolParallel(t *testing.T) {
	s, path := openTemp(t)
	ctx := context.Background()
	if err := s.Upsert(ctx, &Event{Meta: meta("e0", false), Title: "seed", StartAt: ts(9), EndAt: ts(10)}); err != nil {
		t.Fatal(err)
	}

	ro, err := OpenReadOnly(path, 4)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()

	stop := make(chan struct{})
	var writerErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			i++
			ev := &Event{Meta: meta(fmt.Sprintf("writer-%d", i), false), Title: "w", StartAt: ts(9), EndAt: ts(10)}
			if err := s.Upsert(ctx, ev); err != nil {
				writerErr = err
				return
			}
		}
	}()

	var readerWG sync.WaitGroup
	errs := make(chan error, 16*25)
	for g := 0; g < 16; g++ {
		readerWG.Add(1)
		go func() {
			defer readerWG.Done()
			for i := 0; i < 25; i++ {
				if _, err := EventsInRange(ctx, ro, ts(0), ts(23)); err != nil {
					errs <- err
				}
			}
		}()
	}
	readerWG.Wait()
	close(stop)
	wg.Wait()
	close(errs)

	if writerErr != nil {
		t.Fatalf("writer error: %v", writerErr)
	}
	for err := range errs {
		t.Errorf("reader error: %v", err)
	}
}
