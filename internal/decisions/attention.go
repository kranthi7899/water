package decisions

import (
	"strings"

	"water/internal/store"
)

// bulkSenderMarkers and attentionMarkers, and the NeedsAttention logic below,
// are a deliberate byte-for-byte copy of internal/runtime/brief.go's private
// needsAttention heuristic (see trigger.go's package doc for why this is a
// copy, not a call, and what should happen to it next).
var bulkSenderMarkers = []string{"no-reply", "noreply", "do-not-reply", "donotreply", "notifications@", "notification@", "mailer-daemon", "newsletter@"}

var attentionMarkers = []string{"?", "asap", "urgent", "deadline", "by eod", "by end of day", "due ", "please respond", "need your", "can you", "could you", "waiting on you"}

// NeedsAttention is internal/runtime/brief.go's "needs attention" heuristic:
// not from a bulk/no-reply-looking sender, and its subject or body reads
// like it wants a reply (a question mark or one of a small set of
// urgency/deadline phrases). It is the candidate source for classification:
// Candidate below is what Triage's candidate predicate actually uses.
func NeedsAttention(m *store.Message) bool {
	if m == nil {
		return false
	}
	from := strings.ToLower(m.From)
	for _, marker := range bulkSenderMarkers {
		if strings.Contains(from, marker) {
			return false
		}
	}
	text := strings.ToLower(m.Subject + " " + m.Body)
	for _, marker := range attentionMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// Candidate adapts NeedsAttention to the func(store.Record) bool shape
// NewTriager and Trigger require. Only messages are ever candidates, the
// same scope brief.go's own heuristic has today.
func Candidate(r store.Record) bool {
	m, ok := r.(*store.Message)
	return ok && NeedsAttention(m)
}
