package store

import (
	"context"
	"strings"
	"testing"
)

// queryPlan returns SQLite's EXPLAIN QUERY PLAN detail lines for q.
func queryPlan(t *testing.T, s *Store, q string, args ...any) string {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, detail)
	}
	return strings.Join(lines, "\n")
}

// TestRecentWindowQueriesUseIndexes: the brief, the decision trigger and the
// fast path read a recent window (List by created_at, EventsInRange by
// start_at); those must be index range scans with no sort step, not a scan
// of the whole ever-growing table.
func TestRecentWindowQueriesUseIndexes(t *testing.T) {
	s, _ := openTemp(t)
	cases := []struct {
		name, q, index string
	}{
		{"List messages since", "SELECT id FROM messages WHERE created_at >= ? ORDER BY created_at DESC, id DESC", "messages_created_at"},
		{"List events since", "SELECT id FROM events WHERE created_at >= ? ORDER BY created_at DESC, id DESC", "events_created_at"},
		{"EventsInRange", "SELECT id FROM events WHERE start_at >= ? AND start_at < ? ORDER BY start_at ASC", "events_start_at"},
	}
	for _, tc := range cases {
		plan := queryPlan(t, s, tc.q, 1, 2)
		if !strings.Contains(plan, "USING INDEX "+tc.index) && !strings.Contains(plan, "USING COVERING INDEX "+tc.index) {
			t.Errorf("%s: plan does not use %s:\n%s", tc.name, tc.index, plan)
		}
		if strings.Contains(plan, "TEMP B-TREE") {
			t.Errorf("%s: plan still sorts in a temp b-tree:\n%s", tc.name, plan)
		}
	}
}

// TestUpsertNeverDowngradesAFullBody: a preview upsert keeps a stored full
// body (while other fields update), and a later full body still replaces it.
func TestUpsertNeverDowngradesAFullBody(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	up := func(subject, body string, full bool) {
		t.Helper()
		if err := s.Upsert(ctx, &Message{Meta: meta("m1", true), Subject: subject, Body: body, BodyFull: full}); err != nil {
			t.Fatal(err)
		}
	}
	get := func() *Message {
		t.Helper()
		m, err := Get[Message](ctx, s, "fake", "m1")
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	up("s1", "snippet one", false)
	up("s2", "snippet two", false)
	if m := get(); m.Body != "snippet two" || m.BodyFull {
		t.Fatalf("preview over preview: %+v", m)
	}
	up("s3", "the full body", true)
	if m := get(); m.Body != "the full body" || !m.BodyFull {
		t.Fatalf("full over preview: %+v", m)
	}
	up("s4", "snippet three", false)
	if m := get(); m.Body != "the full body" || !m.BodyFull || m.Subject != "s4" {
		t.Fatalf("preview over full must keep the body but update other fields: %+v", m)
	}
	up("s5", "a newer full body", true)
	if m := get(); m.Body != "a newer full body" || !m.BodyFull {
		t.Fatalf("full over full: %+v", m)
	}
}
