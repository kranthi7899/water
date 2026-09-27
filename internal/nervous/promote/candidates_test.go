package promote

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"water/internal/nervous/intents"
	"water/internal/store"
)

func openTemp(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// day1/day2/day3 are noon UTC on three consecutive days. Using noon (never
// near a local midnight boundary in any real-world UTC offset, which spans
// -12 to +14) keeps the "distinct local calendar day" grouping in
// Candidates deterministic regardless of which timezone the test runs in —
// every timestamp shifts by the same offset when converted to local, so a
// 24h gap between two of them always survives as a 1-day gap.
var (
	day1 = time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	day2 = time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
)

// qualifyingSignature is made of two real, Learnable() quick tools
// (reflex.Table()'s store.calendar_events/store.next_event handlers).
const qualifyingSignature = "quick.calendar+quick.next_event"

// baseRow returns a route_log row that, on its own, qualifies for
// candidate grouping (Design §16 item 1's row-level rule): owner=main,
// quick_only, attributed, answered, not a possible miss, a plain lookup
// utterance with no escalate words, and a Learnable()-only signature.
func baseRow(turnID string, at time.Time) store.RouteRow {
	return store.RouteRow{
		TurnID:          turnID,
		At:              at,
		Channel:         "cli",
		Utterance:       "what's on my calendar and when's my next meeting",
		Owner:           "main",
		AnsweredBy:      "main",
		Outcome:         "answered",
		ToolsUsed:       []string{"quick.calendar", "quick.next_event"},
		ToolsAttributed: true,
		QuickOnly:       true,
		ToolSignature:   qualifyingSignature,
	}
}

func insertAll(t *testing.T, s *store.Store, rows []store.RouteRow) {
	t.Helper()
	ctx := context.Background()
	for _, r := range rows {
		if _, err := s.InsertRoute(ctx, r); err != nil {
			t.Fatalf("InsertRoute(%s): %v", r.TurnID, err)
		}
	}
}

func rowsOnDay(prefix string, at time.Time, n int, mutate func(int, *store.RouteRow)) []store.RouteRow {
	out := make([]store.RouteRow, 0, n)
	for i := 0; i < n; i++ {
		r := baseRow(fmt.Sprintf("%s-%d", prefix, i), at)
		if mutate != nil {
			mutate(i, &r)
		}
		out = append(out, r)
	}
	return out
}

func TestCandidatesQualifyingGroupReturned(t *testing.T) {
	s := openTemp(t)
	var rows []store.RouteRow
	rows = append(rows, rowsOnDay("d1", day1, 3, nil)...)
	rows = append(rows, rowsOnDay("d2", day2, 2, nil)...)
	insertAll(t, s, rows)

	got, err := Candidates(context.Background(), s, intents.Shared{}, day1.Add(-time.Hour), 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1: %+v", len(got), got)
	}
	c := got[0]
	if c.Signature != qualifyingSignature {
		t.Fatalf("Signature = %q, want %q", c.Signature, qualifyingSignature)
	}
	if c.Repeats != 5 {
		t.Fatalf("Repeats = %d, want 5", c.Repeats)
	}
	if c.Days != 2 {
		t.Fatalf("Days = %d, want 2", c.Days)
	}
	if c.ID != candidateID(qualifyingSignature) {
		t.Fatalf("ID = %q, want %q", c.ID, candidateID(qualifyingSignature))
	}
	if len(c.SampleTurnIDs) == 0 {
		t.Fatal("SampleTurnIDs is empty")
	}
}

func TestCandidatesFewerThanMinRepeatsExcluded(t *testing.T) {
	s := openTemp(t)
	var rows []store.RouteRow
	rows = append(rows, rowsOnDay("d1", day1, 2, nil)...)
	rows = append(rows, rowsOnDay("d2", day2, 2, nil)...) // 4 total, spans 2 days, still under min
	insertAll(t, s, rows)

	got, err := Candidates(context.Background(), s, intents.Shared{}, day1.Add(-time.Hour), 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d candidates, want 0 (only 4 repeats): %+v", len(got), got)
	}
}

func TestCandidatesSameDayOnlyExcludedEvenWithEnoughRepeats(t *testing.T) {
	s := openTemp(t)
	rows := rowsOnDay("d1", day1, 6, nil) // 6 repeats, but all on one day
	insertAll(t, s, rows)

	got, err := Candidates(context.Background(), s, intents.Shared{}, day1.Add(-time.Hour), 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d candidates, want 0 (only 1 distinct day): %+v", len(got), got)
	}
}

func TestCandidatesPossibleMissRowExcludedButGroupStillQualifies(t *testing.T) {
	s := openTemp(t)
	var rows []store.RouteRow
	// day1: 3 rows, one of them a possible miss.
	rows = append(rows, rowsOnDay("d1", day1, 3, func(i int, r *store.RouteRow) {
		if i == 0 {
			r.PossibleMiss = true
		}
	})...)
	// day2: 3 clean rows.
	rows = append(rows, rowsOnDay("d2", day2, 3, nil)...)
	insertAll(t, s, rows)

	got, err := Candidates(context.Background(), s, intents.Shared{}, day1.Add(-time.Hour), 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1: %+v", len(got), got)
	}
	// 6 rows written, 1 excluded by possible_miss: 5 remain, still spanning
	// both days.
	if got[0].Repeats != 5 {
		t.Fatalf("Repeats = %d, want 5 (the possible-miss row must not count)", got[0].Repeats)
	}
	if got[0].Days != 2 {
		t.Fatalf("Days = %d, want 2", got[0].Days)
	}
}

func TestCandidatesUnattributedRowExcluded(t *testing.T) {
	s := openTemp(t)
	var rows []store.RouteRow
	rows = append(rows, rowsOnDay("d1", day1, 3, func(i int, r *store.RouteRow) {
		if i == 0 {
			r.ToolsAttributed = false
		}
	})...)
	rows = append(rows, rowsOnDay("d2", day2, 3, nil)...)
	insertAll(t, s, rows)

	got, err := Candidates(context.Background(), s, intents.Shared{}, day1.Add(-time.Hour), 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1: %+v", len(got), got)
	}
	if got[0].Repeats != 5 {
		t.Fatalf("Repeats = %d, want 5 (the unattributed row must not count)", got[0].Repeats)
	}
}

// TestCandidatesNonLearnableToolExcluded covers the "a row whose signature
// includes a non-learnable tool" rule. Every real quick.* tool in
// reflex.Table() today happens to be Learnable() (there is no fixture that
// exercises a real non-learnable quick tool), so this exercises the same
// check the way it actually fires in practice: a tool signature naming
// something reflex.Table() doesn't recognize as a quick tool at all is
// never assumed learnable.
func TestCandidatesNonLearnableToolExcluded(t *testing.T) {
	s := openTemp(t)
	var rows []store.RouteRow
	rows = append(rows, rowsOnDay("d1", day1, 3, func(_ int, r *store.RouteRow) {
		r.ToolSignature = "quick.calendar+quick.not_a_real_tool"
	})...)
	rows = append(rows, rowsOnDay("d2", day2, 3, func(_ int, r *store.RouteRow) {
		r.ToolSignature = "quick.calendar+quick.not_a_real_tool"
	})...)
	insertAll(t, s, rows)

	got, err := Candidates(context.Background(), s, intents.Shared{}, day1.Add(-time.Hour), 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d candidates, want 0 (unknown tool in signature): %+v", len(got), got)
	}
}

// TestCandidatesReasoningUtteranceExcludedDefensively is the defensive test
// the design calls for: R-14/R-16's design means a reasoning turn should
// never actually reach route_log with quick_only=1, but Candidates must not
// trust that and re-runs nervous.Eligible itself. One row here is tagged
// with an escalate word despite otherwise looking like a normal qualifying
// row; it must be excluded while its groupmates still count.
func TestCandidatesReasoningUtteranceExcludedDefensively(t *testing.T) {
	s := openTemp(t)
	sh := intents.Shared{EscalateWords: []string{"why"}}

	var rows []store.RouteRow
	rows = append(rows, rowsOnDay("d1", day1, 4, func(i int, r *store.RouteRow) {
		if i == 0 {
			r.Utterance = "why is the board sync more important than the standup"
		}
	})...)
	rows = append(rows, rowsOnDay("d2", day2, 3, nil)...)
	insertAll(t, s, rows)

	got, err := Candidates(context.Background(), s, sh, day1.Add(-time.Hour), 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1: %+v", len(got), got)
	}
	if got[0].Repeats != 6 {
		t.Fatalf("Repeats = %d, want 6 (the reasoning-tagged row must not count)", got[0].Repeats)
	}
}

func TestCandidatesDistinctSignaturesGroupSeparately(t *testing.T) {
	s := openTemp(t)
	var rows []store.RouteRow
	rows = append(rows, rowsOnDay("a-d1", day1, 3, nil)...)
	rows = append(rows, rowsOnDay("a-d2", day2, 2, nil)...)
	otherSig := "quick.latest_mail"
	rows = append(rows, rowsOnDay("b-d1", day1, 3, func(_ int, r *store.RouteRow) {
		r.ToolSignature = otherSig
		r.ToolsUsed = []string{"quick.latest_mail"}
	})...)
	rows = append(rows, rowsOnDay("b-d2", day2, 2, func(_ int, r *store.RouteRow) {
		r.ToolSignature = otherSig
		r.ToolsUsed = []string{"quick.latest_mail"}
	})...)
	insertAll(t, s, rows)

	got, err := Candidates(context.Background(), s, intents.Shared{}, day1.Add(-time.Hour), 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d candidates, want 2: %+v", len(got), got)
	}
	sigs := map[string]bool{got[0].Signature: true, got[1].Signature: true}
	if !sigs[qualifyingSignature] || !sigs[otherSig] {
		t.Fatalf("signatures = %+v, want both groups present", got)
	}
}

func TestCandidateIDStableAndDistinct(t *testing.T) {
	id1a := candidateID(qualifyingSignature)
	id1b := candidateID(qualifyingSignature)
	if id1a != id1b {
		t.Fatalf("candidateID not stable: %q != %q", id1a, id1b)
	}
	if len(id1a) != 12 {
		t.Fatalf("candidateID length = %d, want 12", len(id1a))
	}
	id2 := candidateID("quick.latest_mail")
	if id1a == id2 {
		t.Fatalf("candidateID collided for different signatures: %q", id1a)
	}
}

func TestCandidatesDefaultMinRepeats(t *testing.T) {
	s := openTemp(t)
	rows := append(rowsOnDay("d1", day1, 3, nil), rowsOnDay("d2", day2, 2, nil)...)
	insertAll(t, s, rows)

	// minRepeats <= 0 falls back to DefaultMinRepeats (5): 5 total rows
	// across 2 days still qualifies at the default.
	got, err := Candidates(context.Background(), s, intents.Shared{}, day1.Add(-time.Hour), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d candidates, want 1 at the default min: %+v", len(got), got)
	}
}

// TestCandidatesMaxRowsBoundsTheScan is a regression test: Candidates
// pulled QuickOnlyRoutes' entire result set for the lookback window with no
// row cap at all, so DB I/O and unmarshal cost scaled unboundedly with
// route_log's own size. QuickOnlyRoutes returns newest-first, so a small
// maxRows drops the oldest rows first — here, all of day1's (older)
// qualifying rows fall outside a maxRows of 1 (only day2's single newest
// row survives), so the group no longer spans MinCandidateDays and must
// not qualify, even though it would with maxRows=0 (unlimited, verified by
// TestCandidatesQualifyingGroupReturned's identical row shape above).
func TestCandidatesMaxRowsBoundsTheScan(t *testing.T) {
	s := openTemp(t)
	var rows []store.RouteRow
	rows = append(rows, rowsOnDay("d1", day1, 3, nil)...)
	rows = append(rows, rowsOnDay("d2", day2, 2, nil)...)
	insertAll(t, s, rows)

	got, err := Candidates(context.Background(), s, intents.Shared{}, day1.Add(-time.Hour), 5, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d candidates with maxRows=1, want 0 (only the single newest row is in scope, spanning 1 day < MinCandidateDays): %+v", len(got), got)
	}

	// The same data with maxRows unlimited (0) does qualify, confirming the
	// difference above is the row cap and not some other change.
	unbounded, err := Candidates(context.Background(), s, intents.Shared{}, day1.Add(-time.Hour), 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(unbounded) != 1 {
		t.Fatalf("got %d candidates with maxRows=0, want 1 (unlimited must still see both days)", len(unbounded))
	}
}

// Slice W, D6: general-class rows never contribute to a candidate, both at
// the store filter and in qualifies itself.
func TestCandidatesGeneralRowsExcluded(t *testing.T) {
	s := openTemp(t)
	var rows []store.RouteRow
	rows = append(rows, rowsOnDay("d1", day1, 3, func(i int, r *store.RouteRow) { r.Class = store.RouteClassGeneral })...)
	rows = append(rows, rowsOnDay("d2", day2, 3, func(i int, r *store.RouteRow) { r.Class = store.RouteClassGeneral })...)
	insertAll(t, s, rows)

	got, err := Candidates(context.Background(), s, intents.Shared{}, day1.Add(-time.Hour), 5, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %d candidates from general-only rows, want 0: %+v", len(got), got)
	}

	r := baseRow("direct", day1)
	r.Class = store.RouteClassGeneral
	if qualifies(r, intents.Shared{}, nil, nil) {
		t.Fatal("qualifies accepted a general-class row")
	}
}
