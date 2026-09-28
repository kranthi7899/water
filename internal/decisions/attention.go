package decisions

import (
	"strings"

	"water/internal/mailnoise"
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
// Candidate/CandidateWith below is what Triage's candidate predicate
// actually uses.
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

// signalsFromMessage builds mailnoise's input from a store.Message's own
// bulk-signal fields (migration 0016). mailnoise stays a dependency-free
// leaf (docs/slices/UI.md Phase 0, U21): it never imports internal/store,
// so this package builds the Signals value itself.
func signalsFromMessage(m *store.Message) mailnoise.Signals {
	return mailnoise.Signals{
		Labels:          m.Labels,
		ListUnsubscribe: m.ListUnsubscribe,
		ListID:          m.ListID,
		Precedence:      m.Precedence,
		AutoSubmitted:   m.AutoSubmitted,
	}
}

// CandidateWith returns a candidate predicate like Candidate, additionally
// excluding a message internal/mailnoise.Classify marks noise, given
// wroteTo (typically internal/store.SentToDomain bound to the CEO's own
// addresses — see internal/cli/twin.go's buildDecisionsTrigger).
//
// Order matters here: NewTriager's Triager.Triage checks the candidate
// predicate FIRST, before it ever touches its own in-memory cache or falls
// through to the classifier (StoreCache, which reads
// decision_classifications) — see classify.go's Triage and trigger.go's
// package doc. So a message this predicate excludes is never looked up in
// either cache: a decision_classifications row persisted before this filter
// existed (or before a message accrued its bulk signals) simply stops
// mattering, rather than needing to be invalidated.
func CandidateWith(wroteTo func(domain string) bool) func(store.Record) bool {
	return func(r store.Record) bool {
		m, ok := r.(*store.Message)
		if !ok || !NeedsAttention(m) {
			return false
		}
		v := mailnoise.Classify(m.From, m.Subject, m.Body, signalsFromMessage(m), wroteTo)
		return v.Class != mailnoise.ClassNoise
	}
}

// Candidate is CandidateWith(nil): the CEO's own addresses are unknown, so
// wroteTo is conservatively always false. Kept for callers with no wroteTo
// source wired; production wiring (buildDecisionsTrigger) uses CandidateWith
// directly so a domain the CEO has actually written to is never misclassified.
func Candidate(r store.Record) bool {
	return CandidateWith(nil)(r)
}
