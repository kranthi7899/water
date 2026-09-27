// Package needsyou computes the single "needs you" threshold shared by
// Today and Notifications (docs/slices/V.md §5): the point past which a
// decision card or a pending approval stops being merely-existing and
// starts being something the CEO should be told about. It depends only on
// internal/decisions, internal/approvals and internal/store — never
// internal/runtime, so internal/runtime is free to depend on this package
// later (the same one-way-import discipline internal/decisions/trigger.go
// already documents for its own relationship with internal/runtime).
package needsyou

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"water/internal/approvals"
	"water/internal/decisions"
	"water/internal/recordlinks"
	"water/internal/store"
)

// Item is one thing that has crossed the "needs you" threshold: a decision
// card or a pending approval, normalized to the same shape so Today and
// Notifications can render/sort a single list without caring which.
type Item struct {
	Kind      string // "decision" or "approval"
	ID        string
	Title     string
	Severity  int
	Deadline  *time.Time
	Readiness string
	Untrusted bool
	CreatedAt time.Time
	// Priority is U14's plain-language urgency bucket ("urgent"|"high"|
	// "normal"), computed once here — see priority.go — so native
	// notifications and the web UI's chips share one server-computed value.
	Priority Priority

	// Origin is Phase 3a's one-line "where this came from" note ("From
	// Meridian renewal", "Requested by Lee", "Built from an outside email"),
	// computed once here — see origin.go's itemOrigin — from the decision
	// card or approval envelope this Item was built from. "" when none of
	// origin.go's three cases apply. Always server-built: Today never
	// guesses this client-side.
	Origin string
	// OriginKind mirrors approvals.Envelope.OriginKind ("agent_draft" |
	// "person_request") for an approval item, and is "" for a decision
	// item. It rides alongside Origin (Phase 3a) so Today's kind icon can
	// tell an agent-drafted approval from a person's request without
	// parsing Origin's text.
	OriginKind string

	// sourceItemIDs is a decision card's SourceItemIDs, kept (unexported,
	// so never serialized into Today's JSON) for Tick's involves links.
	sourceItemIDs []string
}

const (
	KindDecision = "decision"
	KindApproval = "approval"
)

// DecisionSource is what Compute needs from the decision layer: exactly
// decisions.Trigger's own Run signature (a *decisions.Trigger satisfies
// this directly), so a caller wires the real trigger in and a test wires in
// a fake with canned cards/error.
type DecisionSource interface {
	Run(ctx context.Context, now time.Time) ([]*decisions.Card, error)
}

// Compute builds the current "needs you" list: candidate decision cards
// from src, minus anything already dismissed (store.DismissedCardIDs),
// minus anything under minSeverity; candidate pending approvals from q,
// minus anything not yet past approvalGrace. Neither source's error is ever
// swallowed — a failure here must surface as "we don't know", never
// silently as "nothing needs you".
//
// The merged list is deliberately two blocks, not a single numeric
// interleave: every decision item sorts before every approval item.
// Decisions and approvals don't share a comparable urgency scale (a
// decision's Severity is an author-set weight; an approval's urgency is
// purely its age), so inventing a cross-kind comparison would be asserting
// a conversion nothing in this codebase defines. Within each block:
//   - decisions: decisions.Rank's existing ordering (severity descending,
//     then deadline ascending, nil deadline last) — reused, not
//     reimplemented.
//   - approvals: oldest first (a longer-pending approval is more overdue
//     for an answer).
func Compute(ctx context.Context, src DecisionSource, q *approvals.Queue, st *store.Store, now time.Time, minSeverity int, approvalGrace time.Duration) ([]Item, error) {
	if src == nil || q == nil || st == nil {
		return nil, fmt.Errorf("needsyou: compute needs a decision source, an approval queue and a store")
	}
	cards, err := src.Run(ctx, now)
	if err != nil {
		return nil, fmt.Errorf("needsyou: decision source: %w", err)
	}
	// A persisted decision_records row is authoritative over whatever the
	// live classifier just computed for the same card id (decisions.Merge,
	// docs/slices/UI.md Phase 1c, U13) -- the same merge GET /v1/decisions
	// applies, so "needs you" and the decisions list never disagree about
	// which version of a card is current.
	cards, err = decisions.MergeFromStore(ctx, st, cards)
	if err != nil {
		return nil, fmt.Errorf("needsyou: merging decision records: %w", err)
	}
	dismissed, err := st.DismissedCardIDs(ctx)
	if err != nil {
		return nil, fmt.Errorf("needsyou: dismissed card ids: %w", err)
	}
	// cardTitles resolves an approval's SourceCardID to that card's own
	// title (itemFromEnvelope's Origin, "From <decision>"): built from every
	// merged card, not just kept -- a card dismissed from Today or below
	// minSeverity is still a perfectly good answer to "what decision did
	// this approval come from".
	cardTitles := make(map[string]string, len(cards))
	for _, c := range cards {
		if c != nil {
			cardTitles[c.ID] = cardTitle(c)
		}
	}

	var kept []*decisions.Card
	for _, c := range cards {
		if c == nil || dismissed[c.ID] || c.Severity < minSeverity {
			continue
		}
		kept = append(kept, c)
	}
	decisions.Rank(kept)

	pending, err := q.Pending(ctx)
	if err != nil {
		return nil, fmt.Errorf("needsyou: pending approvals: %w", err)
	}
	var envelopes []approvals.Envelope
	for _, e := range pending {
		if now.Sub(e.CreatedAt) >= approvalGrace {
			envelopes = append(envelopes, e)
		}
	}
	sort.SliceStable(envelopes, func(i, j int) bool { return envelopes[i].CreatedAt.Before(envelopes[j].CreatedAt) })

	out := make([]Item, 0, len(kept)+len(envelopes))
	for _, c := range kept {
		out = append(out, itemFromCard(c, now))
	}
	for _, e := range envelopes {
		// Best-effort: an unresolvable or absent requester just means no
		// "Requested by" line, never a failed Compute call (a roster lookup
		// is not something the CEO should ever see "needs you" itself fail
		// over).
		name := requesterName(ctx, st, e.RequestedBy)
		out = append(out, itemFromEnvelope(e, now, cardTitles[e.SourceCardID], name))
	}
	return out, nil
}

// cardTitle is a decision card's own display title (its Lead, falling back
// to Question) -- shared by itemFromCard's Title and Compute's cardTitles
// map, so "From <decision>" never disagrees with what Today calls the same
// card when it appears there directly.
func cardTitle(c *decisions.Card) string {
	if c.Lead != "" {
		return c.Lead
	}
	return c.Question
}

// requesterName resolves a person-request approval's RequestedBy (a roster
// person id, e.g. "lee") to that person's display name via the roster's
// "people" table (internal/roster). "" on any miss -- an empty, malformed
// or absent identities value, an id no longer in the roster -- which is a
// resolution miss, not a caller-visible error (the same posture
// store.Person.Identity documents for its own lookups).
func requesterName(ctx context.Context, st *store.Store, requestedBy string) string {
	if requestedBy == "" {
		return ""
	}
	p, err := store.Get[store.Person](ctx, st, "seed", requestedBy)
	if err != nil {
		return ""
	}
	return p.Name
}

// itemFromCard maps a decision card to an Item. Card carries no creation
// timestamp of its own (cards are rebuilt fresh on every Trigger.Run, never
// persisted — decisions/card.go's own doc comment), so CreatedAt is set to
// now: when this Compute call observed the card, not when it first existed.
func itemFromCard(c *decisions.Card, now time.Time) Item {
	return Item{
		Kind:      KindDecision,
		ID:        c.ID,
		Title:     cardTitle(c),
		Severity:  c.Severity,
		Deadline:  c.Deadline,
		Readiness: string(c.Readiness),
		Untrusted: c.Untrusted,
		CreatedAt: now,
		Priority:  DecisionPriority(c.Severity, c.Deadline, now),
		// A decision card never traces to another card and has no
		// requester concept, so its only possible Origin is origin.go's
		// third case: untrusted, with nothing more specific to name.
		Origin: itemOrigin("", "", c.Untrusted),

		sourceItemIDs: append([]string(nil), c.SourceItemIDs...),
	}
}

// itemFromEnvelope maps a pending approval to an Item. An envelope has no
// severity, readiness or deadline concept, so those are left at their zero
// values; Title is the action id (Envelope has no separate human-readable
// name). Priority follows U14's approval sub-rule (ApprovalPriority): risk
// high or a money-shaped payload is High; Envelope carries no deadline
// today, so the "deadline <= 3 days -> Urgent" half of the rule never fires
// here yet (there is nothing to read it from).
func itemFromEnvelope(e approvals.Envelope, now time.Time, sourceCardTitle, requestedByName string) Item {
	return Item{
		Kind:       KindApproval,
		ID:         e.ID,
		Title:      e.Action,
		CreatedAt:  e.CreatedAt,
		Priority:   ApprovalPriority(e.Risk == "high", PayloadLooksLikeMoney(e.Payload), nil, now),
		OriginKind: e.OriginKind,
		// An approval has no untrusted signal of its own (Envelope carries
		// no such field): its Origin is either "From <decision>" (it traces
		// to one via SourceCardID) or "Requested by <name>" (it's a person
		// request), never origin.go's untrusted case.
		Origin: itemOrigin(sourceCardTitle, requestedByName, false),
	}
}

// Service holds the last computed "needs you" snapshot and the store/queue/
// source/thresholds Tick recomputes it from. Today reads Snapshot(), never
// recomputing Trigger.Run itself on every poll (the plan's own cost
// warning, docs/slices/V.md §5); Tick is what actually recomputes, driven
// by a background loop at notify.interval_seconds.
type Service struct {
	mu       sync.RWMutex
	snapshot []Item

	src           DecisionSource
	q             *approvals.Queue
	st            *store.Store
	minSeverity   int
	approvalGrace time.Duration
}

// NewService builds a Service. It computes nothing until the first Tick.
func NewService(src DecisionSource, q *approvals.Queue, st *store.Store, minSeverity int, approvalGrace time.Duration) *Service {
	return &Service{src: src, q: q, st: st, minSeverity: minSeverity, approvalGrace: approvalGrace}
}

// Tick recomputes the snapshot and, for every item in it, records a
// notification if this is the first time that record has ever crossed the
// threshold. The store's UNIQUE (record_type, record_id) constraint
// (InsertNotificationIfNew) is the actual source of truth for "already
// notified", not any in-memory state here — that's what makes "notify once
// per record, ever" survive a daemon restart even though the snapshot
// itself does not.
//
// Each decision item is also linked decision -> involves -> person for its
// source message's sender, when the roster resolves that sender by email
// (docs/slices/V.md D6; recordlinks.LinkDecision). The write is idempotent,
// so re-linking on every tick adds nothing new. A link failure never stops
// the remaining notifications: failures are collected and returned after
// every item has been handled.
func (s *Service) Tick(ctx context.Context, now time.Time) error {
	items, err := Compute(ctx, s.src, s.q, s.st, now, s.minSeverity, s.approvalGrace)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.snapshot = items
	s.mu.Unlock()

	for _, item := range items {
		title, body := notificationText(item)
		if _, err := s.st.InsertNotificationIfNew(ctx, store.Notification{
			RecordType: item.Kind,
			RecordID:   item.ID,
			Title:      title,
			Body:       body,
		}); err != nil {
			return fmt.Errorf("needsyou: recording notification for %s %s: %w", item.Kind, item.ID, err)
		}
	}
	var linkErrs []error
	for _, item := range items {
		if item.Kind != KindDecision {
			continue
		}
		if _, err := recordlinks.LinkDecision(ctx, s.st, item.ID, item.sourceItemIDs); err != nil {
			linkErrs = append(linkErrs, fmt.Errorf("needsyou: linking decision %s: %w", item.ID, err))
		}
	}
	return errors.Join(linkErrs...)
}

// notificationText builds a native-notification Title/Body from an Item's
// own fields only (Item carries nothing beyond what's documented on it).
func notificationText(item Item) (title, body string) {
	switch item.Kind {
	case KindApproval:
		body = "Pending your approval since " + item.CreatedAt.Local().Format("Mon 2006-01-02 15:04")
		return item.Title, body
	default: // KindDecision
		body = fmt.Sprintf("Severity %d, %s", item.Severity, readinessLabel(item.Readiness))
		if item.Deadline != nil {
			body += ", due " + item.Deadline.Local().Format("Mon 2006-01-02 15:04")
		}
		return item.Title, body
	}
}

func readinessLabel(r string) string {
	if r == "" {
		return "readiness unknown"
	}
	return "readiness " + r
}

// Snapshot returns a copy of the last computed list (safe for the caller to
// mutate without touching the Service's own state).
func (s *Service) Snapshot() []Item {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Item, len(s.snapshot))
	copy(out, s.snapshot)
	return out
}
