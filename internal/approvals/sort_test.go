package approvals

import (
	"sort"
	"testing"
	"time"
)

func TestPriorityRank(t *testing.T) {
	cases := []struct {
		priority string
		want     int
	}{
		{"urgent", 0}, {"high", 1}, {"normal", 2}, {"", 2}, {"bogus", 2},
	}
	for _, c := range cases {
		if got := PriorityRank(c.priority); got != c.want {
			t.Errorf("PriorityRank(%q) = %d, want %d", c.priority, got, c.want)
		}
	}
}

func TestLessForQueuePriorityFirst(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	urgent := Envelope{ID: "e-urgent", Priority: "urgent", CreatedAt: now}
	high := Envelope{ID: "e-high", Priority: "high", CreatedAt: now}
	normal := Envelope{ID: "e-normal", Priority: "normal", CreatedAt: now}
	if !LessForQueue(urgent, high) || LessForQueue(high, urgent) {
		t.Error("urgent must sort before high")
	}
	if !LessForQueue(high, normal) || LessForQueue(normal, high) {
		t.Error("high must sort before normal")
	}
}

func TestLessForQueueDeadlineSecond(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	soon := Envelope{ID: "e-soon", Priority: "high", Deadline: now.Add(24 * time.Hour), CreatedAt: now}
	later := Envelope{ID: "e-later", Priority: "high", Deadline: now.Add(72 * time.Hour), CreatedAt: now}
	none := Envelope{ID: "e-none", Priority: "high", CreatedAt: now}
	if !LessForQueue(soon, later) || LessForQueue(later, soon) {
		t.Error("a sooner deadline must sort first, same priority")
	}
	if !LessForQueue(soon, none) || LessForQueue(none, soon) {
		t.Error("a set deadline must sort before no deadline, same priority")
	}
}

// TestLessForQueueStableForEqualKeys: two envelopes tied on priority and
// deadline break the tie by CreatedAt (oldest first), and sort.SliceStable
// over LessForQueue produces the same order regardless of the input slice's
// starting order.
func TestLessForQueueStableForEqualKeys(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	older := Envelope{ID: "e-older", Priority: "urgent", CreatedAt: now.Add(-time.Hour)}
	newer := Envelope{ID: "e-newer", Priority: "urgent", CreatedAt: now}
	for _, start := range [][]Envelope{{older, newer}, {newer, older}} {
		got := append([]Envelope(nil), start...)
		sort.SliceStable(got, func(i, j int) bool { return LessForQueue(got[i], got[j]) })
		if got[0].ID != "e-older" || got[1].ID != "e-newer" {
			t.Errorf("start %v: sorted = [%s, %s], want [e-older, e-newer]", start, got[0].ID, got[1].ID)
		}
	}
}
