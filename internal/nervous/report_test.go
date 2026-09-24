package nervous

import (
	"testing"
	"time"

	"water/internal/store"
)

func i64(v int64) *int64 { return &v }

func TestBuildReportPercentilesByHand(t *testing.T) {
	// Ten rows, t0 latency 10,20,...,100ms: p50 (nearest-rank at index
	// floor(0.5*9)=4) is the 5th sorted value (50ms), p95 (index
	// floor(0.95*9)=8) is the 9th sorted value (90ms).
	var rows []store.RouteRow
	for i := 1; i <= 10; i++ {
		rows = append(rows, store.RouteRow{
			LatencyMS: map[string]int64{"t0": int64(i) * 10},
			TotalMS:   int64(i) * 10,
			Outcome:   "answered",
		})
	}
	rep := BuildReport(rows)

	if got, want := rep.LatencyP50["t0"], 50*time.Millisecond; got != want {
		t.Errorf("LatencyP50[t0] = %v, want %v", got, want)
	}
	if got, want := rep.LatencyP95["t0"], 90*time.Millisecond; got != want {
		t.Errorf("LatencyP95[t0] = %v, want %v", got, want)
	}
	if got, want := rep.TotalP50, 50*time.Millisecond; got != want {
		t.Errorf("TotalP50 = %v, want %v", got, want)
	}
	if rep.N != 10 {
		t.Errorf("N = %d, want 10", rep.N)
	}
}

func TestBuildReportHistogramsAndCounts(t *testing.T) {
	rows := []store.RouteRow{
		{Owner: "quick", AnsweredBy: "t0", Outcome: "answered", TiersAttempted: []string{"t0"}},
		{Owner: "quick", AnsweredBy: "t0", Outcome: "answered", TiersAttempted: []string{"t0"}, PossibleMiss: true},
		{Owner: "main", AnsweredBy: "main", Outcome: "answered", TiersAttempted: []string{"t0", "main"}, EscalationReason: "escalate_word:should"},
		{Owner: "main", AnsweredBy: "main", Outcome: "answered", TiersAttempted: []string{"t0", "main"}, EscalationReason: "escalate_word:why"},
		{Owner: "main", AnsweredBy: "main", Outcome: "error", TiersAttempted: []string{"t0", "main"}, EscalationReason: "no_match"},
		{Owner: "", AnsweredBy: "", Outcome: "cancelled", TiersAttempted: []string{"t0"}, EscalationReason: "intent_inactive:mail.send_reply"},
	}
	rep := BuildReport(rows)

	if rep.OwnerCounts["quick"] != 2 || rep.OwnerCounts["main"] != 3 {
		t.Errorf("OwnerCounts = %+v", rep.OwnerCounts)
	}
	if rep.TierCounts["t0"] != 6 || rep.TierCounts["main"] != 3 {
		t.Errorf("TierCounts = %+v", rep.TierCounts)
	}
	if rep.OutcomeCounts["answered"] != 4 || rep.OutcomeCounts["error"] != 1 || rep.OutcomeCounts["cancelled"] != 1 {
		t.Errorf("OutcomeCounts = %+v", rep.OutcomeCounts)
	}
	// Both "escalate_word:should" and "escalate_word:why" collapse into one
	// "escalate_word" bucket, per Design §14's "escalation-reason histogram,
	// including escalate_word:* grouped."
	if rep.EscalationReasons["escalate_word"] != 2 {
		t.Errorf("EscalationReasons[escalate_word] = %d, want 2 (got %+v)", rep.EscalationReasons["escalate_word"], rep.EscalationReasons)
	}
	if rep.EscalationReasons["no_match"] != 1 {
		t.Errorf("EscalationReasons[no_match] = %d, want 1", rep.EscalationReasons["no_match"])
	}
	if rep.InactiveIntentNearMiss["mail.send_reply"] != 1 {
		t.Errorf("InactiveIntentNearMiss[mail.send_reply] = %d, want 1 (got %+v)", rep.InactiveIntentNearMiss["mail.send_reply"], rep.InactiveIntentNearMiss)
	}
	if rep.PossibleMissCount != 1 {
		t.Errorf("PossibleMissCount = %d, want 1", rep.PossibleMissCount)
	}
	if len(rep.RecentPossibleMisses) != 1 {
		t.Errorf("RecentPossibleMisses = %d rows, want 1", len(rep.RecentPossibleMisses))
	}
}

func TestBuildReportAckAndLintAndSpeculation(t *testing.T) {
	rows := []store.RouteRow{
		{Outcome: "answered", AckMS: i64(100), FirstSentenceMS: i64(500), Warnings: []string{"banned:as an ai", "overlength"}},
		{Outcome: "answered", AckMS: i64(200), FirstSentenceMS: i64(700), Warnings: []string{"overlength"}},
		{Outcome: "answered", Partials: 3, Speculation: map[string]any{"reused": true}},
		{Outcome: "answered", Partials: 0, Speculation: map[string]any{"reused": false}},
		{Outcome: "answered"}, // no speculation data at all: excluded from the reuse-rate denominator
	}
	rep := BuildReport(rows)

	if rep.AckP50 != 100*time.Millisecond && rep.AckP50 != 200*time.Millisecond {
		t.Errorf("AckP50 = %v, want one of the two recorded values", rep.AckP50)
	}
	if rep.LintWarningCounts["overlength"] != 2 {
		t.Errorf("LintWarningCounts[overlength] = %d, want 2", rep.LintWarningCounts["overlength"])
	}
	if rep.LintWarningCounts["banned:as an ai"] != 1 {
		t.Errorf("LintWarningCounts[banned:as an ai] = %d, want 1", rep.LintWarningCounts["banned:as an ai"])
	}
	if rep.TurnsWithPartials != 1 {
		t.Errorf("TurnsWithPartials = %d, want 1", rep.TurnsWithPartials)
	}
	if got, want := rep.SpeculationReuseRate, 0.5; got != want {
		t.Errorf("SpeculationReuseRate = %v, want %v (1 reused of 2 rows with any speculation data)", got, want)
	}
}

func TestBuildReportEmpty(t *testing.T) {
	rep := BuildReport(nil)
	if rep.N != 0 {
		t.Fatalf("N = %d, want 0", rep.N)
	}
	if rep.TotalP50 != 0 || rep.AckP50 != 0 {
		t.Fatalf("percentiles over no data should be zero, got TotalP50=%v AckP50=%v", rep.TotalP50, rep.AckP50)
	}
}
