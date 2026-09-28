package decisions

import (
	"testing"
	"time"
)

func TestRankOrdersBySeverityThenDeadline(t *testing.T) {
	now := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
	soon := now.Add(24 * time.Hour)
	later := now.Add(72 * time.Hour)

	noDeadline := &Card{ID: "sev2-none", Severity: 2}
	sev2Later := &Card{ID: "sev2-later", Severity: 2, Deadline: &later}
	sev2Soon := &Card{ID: "sev2-soon", Severity: 2, Deadline: &soon}
	sev1 := &Card{ID: "sev1", Severity: 1, Deadline: &soon}
	sev3 := &Card{ID: "sev3", Severity: 3}

	cards := Rank([]*Card{noDeadline, sev2Later, sev1, sev2Soon, sev3})

	var ids []string
	for _, c := range cards {
		ids = append(ids, c.ID)
	}
	want := []string{"sev3", "sev2-soon", "sev2-later", "sev2-none", "sev1"}
	for i, id := range want {
		if ids[i] != id {
			t.Fatalf("Rank order = %v, want %v", ids, want)
		}
	}
}

func TestRankIsStableForEqualCards(t *testing.T) {
	a := &Card{ID: "a", Severity: 1}
	b := &Card{ID: "b", Severity: 1}
	cards := Rank([]*Card{a, b})
	if cards[0].ID != "a" || cards[1].ID != "b" {
		t.Fatalf("Rank should be stable for equal cards, got %s, %s", cards[0].ID, cards[1].ID)
	}
}
