package nervous

import (
	"sort"
	"strings"
	"time"

	"water/internal/store"
)

// Report is the read-only aggregation `water route report` (a later task's
// CLI/HTTP surface) presents. BuildReport computes it entirely from rows
// store.ListRoutes/QuickOnlyRoutes already returns — there is no new
// storage here, only arithmetic over existing route_log data.
type Report struct {
	N                      int
	OwnerCounts            map[string]int
	TierCounts             map[string]int // a tier counted once per turn that attempted it
	AnsweredByCounts       map[string]int
	OutcomeCounts          map[string]int
	EscalationReasons      map[string]int // escalate_word:<x> variants collapse into one "escalate_word" bucket
	LatencyP50             map[string]time.Duration
	LatencyP95             map[string]time.Duration
	LatencyP99             map[string]time.Duration
	TotalP50, TotalP95     time.Duration
	AckP50, AckP95         time.Duration
	FirstSentenceP50       time.Duration
	FirstSentenceP95       time.Duration
	TurnsWithPartials      int
	SpeculationReuseRate   float64 // reused/attempted among rows that recorded any speculation data
	LintWarningCounts      map[string]int
	PossibleMissCount      int
	RecentPossibleMisses   []store.RouteRow // newest first, capped at 20
	QuickToolUsageCounts   map[string]int
	InactiveIntentNearMiss map[string]int // escalation_reason "intent_inactive:<id>" -> count, keyed by <id>
}

// BuildReport aggregates rows (as store.ListRoutes returns them: any order
// is accepted, this function sorts what it needs internally) into a Report.
func BuildReport(rows []store.RouteRow) Report {
	r := Report{
		N:                      len(rows),
		OwnerCounts:            map[string]int{},
		TierCounts:             map[string]int{},
		AnsweredByCounts:       map[string]int{},
		OutcomeCounts:          map[string]int{},
		EscalationReasons:      map[string]int{},
		LatencyP50:             map[string]time.Duration{},
		LatencyP95:             map[string]time.Duration{},
		LatencyP99:             map[string]time.Duration{},
		LintWarningCounts:      map[string]int{},
		QuickToolUsageCounts:   map[string]int{},
		InactiveIntentNearMiss: map[string]int{},
	}

	latencies := map[string][]time.Duration{}
	var totals, acks, firstSentences []time.Duration
	var specAttempted, specReused int

	for _, row := range rows {
		r.OwnerCounts[row.Owner]++
		r.AnsweredByCounts[row.AnsweredBy]++
		r.OutcomeCounts[row.Outcome]++
		for _, t := range row.TiersAttempted {
			r.TierCounts[t]++
		}

		if row.EscalationReason != "" {
			key := row.EscalationReason
			if strings.HasPrefix(key, "escalate_word:") {
				key = "escalate_word"
			}
			r.EscalationReasons[key]++
			if id, ok := strings.CutPrefix(row.EscalationReason, "intent_inactive:"); ok {
				r.InactiveIntentNearMiss[id]++
			}
		}

		for tier, ms := range row.LatencyMS {
			latencies[tier] = append(latencies[tier], time.Duration(ms)*time.Millisecond)
		}
		if row.TotalMS > 0 {
			totals = append(totals, time.Duration(row.TotalMS)*time.Millisecond)
		}
		if row.AckMS != nil {
			acks = append(acks, time.Duration(*row.AckMS)*time.Millisecond)
		}
		if row.FirstSentenceMS != nil {
			firstSentences = append(firstSentences, time.Duration(*row.FirstSentenceMS)*time.Millisecond)
		}

		for _, w := range row.Warnings {
			r.LintWarningCounts[w]++
		}

		if row.Partials > 0 {
			r.TurnsWithPartials++
		}
		if row.Speculation != nil {
			specAttempted++
			if reused, ok := row.Speculation["reused"].(bool); ok && reused {
				specReused++
			}
		}

		for _, tool := range row.ToolsUsed {
			r.QuickToolUsageCounts[tool]++
		}

		if row.PossibleMiss {
			r.PossibleMissCount++
			if len(r.RecentPossibleMisses) < 20 {
				r.RecentPossibleMisses = append(r.RecentPossibleMisses, row)
			}
		}
	}

	for tier, ds := range latencies {
		r.LatencyP50[tier] = Percentile(ds, 0.50)
		r.LatencyP95[tier] = Percentile(ds, 0.95)
		r.LatencyP99[tier] = Percentile(ds, 0.99)
	}
	r.TotalP50, r.TotalP95 = Percentile(totals, 0.50), Percentile(totals, 0.95)
	r.AckP50, r.AckP95 = Percentile(acks, 0.50), Percentile(acks, 0.95)
	r.FirstSentenceP50, r.FirstSentenceP95 = Percentile(firstSentences, 0.50), Percentile(firstSentences, 0.95)
	if specAttempted > 0 {
		r.SpeculationReuseRate = float64(specReused) / float64(specAttempted)
	}

	return r
}

// Percentile uses nearest-rank on a copy of ds (ds itself is never mutated,
// and it need not already be sorted). An empty input reports zero rather
// than panicking, since most tiers won't have data in every window.
// Exported so internal/nervous/eval shares this exact implementation
// instead of maintaining its own — the two independently drifted apart
// before this (a different index formula, ceil(p*n)-1 vs this file's
// int(p*(n-1)), provably disagree for the same input; e.g. n=10, p=0.95
// gives index 9 vs 8) — a real risk if a duplicate is ever reused or
// surfaced elsewhere, even though neither had a currently-visible
// incorrect-output bug from it.
func Percentile(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), ds...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := int(p * float64(len(sorted)-1))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
