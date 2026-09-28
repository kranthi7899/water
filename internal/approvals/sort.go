package approvals

// PriorityRank orders an Envelope.Priority value so the Approvals queue's
// most urgent rows sort first: "urgent" (0) before "high" (1) before
// "normal" or any other/empty value (2). This mirrors needsyou.Priority's
// three buckets (U14) as plain strings rather than importing that type:
// internal/needsyou already imports internal/approvals (needsyou.go's own
// package doc says so), so the reverse import would be circular.
func PriorityRank(priority string) int {
	switch priority {
	case "urgent":
		return 0
	case "high":
		return 1
	default:
		return 2
	}
}

// LessForQueue orders two envelopes for the Approvals queue's dense rows
// (docs/slices/UI.md Phase 3a: "sorted client-side by priority, then
// deadline"): PriorityRank first (urgent, then high, then normal/unset),
// then Deadline ascending with a set deadline always sorting before no
// deadline at all, then CreatedAt ascending (oldest first) as the final,
// deterministic tiebreaker so two envelopes tied on both never compare
// equal by chance ordering.
//
// This is the shared, directly-testable rule; view_approvals.js implements
// the identical comparator client-side (see its own header comment) rather
// than this function being called from an HTTP handler, so the "sorted
// client-side" requirement and the "give it a testable Go shape" one are
// both met without duplicating meaning, only the small amount of logic.
func LessForQueue(a, b Envelope) bool {
	if ra, rb := PriorityRank(a.Priority), PriorityRank(b.Priority); ra != rb {
		return ra < rb
	}
	az, bz := a.Deadline.IsZero(), b.Deadline.IsZero()
	if az != bz {
		return bz // a has a deadline and b doesn't: a sorts first
	}
	if !az && !a.Deadline.Equal(b.Deadline) {
		return a.Deadline.Before(b.Deadline)
	}
	return a.CreatedAt.Before(b.CreatedAt)
}
