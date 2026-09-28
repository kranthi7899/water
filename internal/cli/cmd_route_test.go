package cli

import (
	"strings"
	"testing"
	"time"

	"water/internal/nervous"
	"water/internal/nervous/promote"
)

func TestParseSinceFlag(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"30d", 30 * 24 * time.Hour, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"168h", 168 * time.Hour, false},
		{"1h30m", 90 * time.Minute, false},
		{"0d", 0, true},
		{"-5d", 0, true},
		{"not-a-duration", 0, true},
		{"", 0, true},
	}
	for _, tc := range cases {
		got, err := parseSinceFlag(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("parseSinceFlag(%q) = %v, nil; want an error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseSinceFlag(%q) unexpected error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseSinceFlag(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestRenderCandidatesFromCannedJSON exercises the CLI's own rendering
// logic directly against canned promote.Candidate data (the same "rendering
// from canned JSON" pattern docs/slices/R.md's R-26 entry describes for
// this file's other list commands), rather than standing up a daemon.
func TestRenderCandidatesFromCannedJSON(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		got := renderCandidates(nil)
		if !strings.Contains(got, "no promotion candidates") {
			t.Fatalf("renderCandidates(nil) = %q, want a no-candidates message", got)
		}
	})

	t.Run("non-empty table", func(t *testing.T) {
		cands := []promote.Candidate{
			{
				ID:            "abc123def456",
				Signature:     "quick.calendar+quick.next_event",
				Repeats:       7,
				Days:          3,
				SampleTurnIDs: []string{"t1", "t2"},
			},
			{
				ID:        "aaaaaaaaaaaa",
				Signature: "quick.latest_mail",
				Repeats:   5,
				Days:      2,
			},
		}
		got := renderCandidates(cands)
		for _, want := range []string{
			"ID", "REPEATS", "DAYS", "SIGNATURE",
			"abc123def456", "quick.calendar+quick.next_event", "7", "3",
			"aaaaaaaaaaaa", "quick.latest_mail", "5", "2",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("renderCandidates output missing %q:\n%s", want, got)
			}
		}
		lines := strings.Split(strings.TrimRight(got, "\n"), "\n")
		if len(lines) != 3 { // header + 2 rows
			t.Fatalf("got %d lines, want 3 (header + 2 rows):\n%s", len(lines), got)
		}
	})
}

// TestRenderRouteReportFromCannedJSON exercises `water route report`'s
// rendering directly against a hand-built nervous.Report, the same
// "rendering from canned JSON" pattern as TestRenderCandidatesFromCannedJSON
// above.
func TestRenderRouteReportFromCannedJSON(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		got := renderRouteReport(nervous.Report{})
		if !strings.Contains(got, "no route_log rows") {
			t.Fatalf("renderRouteReport(zero) = %q, want a no-rows message", got)
		}
	})

	t.Run("populated", func(t *testing.T) {
		rep := nervous.Report{
			N:                 42,
			TierCounts:        map[string]int{"tier0": 30, "main": 12},
			OwnerCounts:       map[string]int{"main": 42},
			OutcomeCounts:     map[string]int{"answered": 40, "escalated": 2},
			EscalationReasons: map[string]int{"ambiguous_match": 2},
			TotalP50:          120 * time.Millisecond,
			TotalP95:          400 * time.Millisecond,
			AckP50:            20 * time.Millisecond,
			AckP95:            60 * time.Millisecond,
			QuickToolUsageCounts: map[string]int{
				"quick.calendar": 10,
			},
			PossibleMissCount: 3,
			InactiveIntentNearMiss: map[string]int{
				"control.stop": 1,
			},
		}
		got := renderRouteReport(rep)
		for _, want := range []string{
			"42", "tier0=30", "main=12", "answered=40", "escalated=2",
			"ambiguous_match=2", "quick.calendar=10", "possible misses", "3",
			"control.stop=1",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("renderRouteReport output missing %q:\n%s", want, got)
			}
		}
	})
}

func TestFormatCounts(t *testing.T) {
	if got := formatCounts(nil); got != "(none)" {
		t.Fatalf("formatCounts(nil) = %q, want (none)", got)
	}
	got := formatCounts(map[string]int{"b": 2, "a": 1})
	if got != "a=1, b=2" {
		t.Fatalf("formatCounts = %q, want sorted a=1, b=2", got)
	}
}
