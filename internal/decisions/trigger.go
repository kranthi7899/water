package decisions

// This file answers a question `docs/slices/C.md` and
// `docs/slice-c-planning.md` deliberately leave open: WHEN does Classify
// actually run, and on WHAT items? Classifying every synced record would be
// wasteful (most mail, meetings and documents never need a CEO decision) and
// expensive (one model call per item). The design filled in here:
//
//  1. An item is only ever a classification candidate if the cheap,
//     code-only "needs attention" heuristic already flags it — the same
//     heuristic `internal/runtime/brief.go` computes to decide which
//     messages the morning brief calls out (see attention.go). Nothing
//     else is a candidate; a record type brief.go doesn't look at (a
//     calendar event, a commit, a contact) is never classified by Trigger.
//  2. Each candidate is classified at most once, ever, via two layered
//     caches: Triager's own in-memory cache (classify.go, dedup within one
//     process/run) sits in front of StoreCache below, which persists the
//     verdict in the store's decision_classifications table
//     (internal/store/decision_classifications.go, migration 0004) keyed by
//     the item's own (Source, SourceID). A later sync tick, or a daemon
//     restart, never re-classifies the same item. Two exceptions: a
//     Fallback verdict (the model's reply was unreadable) is never
//     persisted and is retried after Triager.FallbackRetry, and
//     Triager.Forget clears both layers so the item is asked again.
//
// A note on attention.go's duplication. This package cannot import
// internal/runtime to call its private needsAttention directly (it's
// unexported, and this task's scope does not permit editing brief.go to
// export it). Separately, C.md says the morning brief itself gains a new
// signal from this package ("ranked open cards" over Card.Severity/
// Deadline) — once that lands, internal/runtime will import
// internal/decisions, so internal/decisions must not import
// internal/runtime in the other direction. attention.go's NeedsAttention is
// therefore a deliberate, documented copy of brief.go's heuristic, not the
// start of an import cycle. The two copies must be kept in sync until
// whoever wires the brief/CLI next (this task's explicit follow-up) deletes
// brief.go's private needsAttention and has it call decisions.Candidate
// instead — the natural point to do that is exactly when brief.go starts
// importing this package for the new open-cards signal anyway.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"water/internal/store"
)

// StoreCache wraps a Classifier with a durable cache keyed by an item's own
// (Source, SourceID) identity, persisted via the store's
// decision_classifications table. It is what makes "classify an item at
// most once" survive a daemon restart; Triager's own cache only dedups
// within one process. An item with no store identity (Ref returns "") is
// never cached and always falls through to Inner.
type StoreCache struct {
	Store *store.Store
	Inner Classifier
}

// Classify implements Classifier.
func (c *StoreCache) Classify(ctx context.Context, item store.Record) (Classification, error) {
	if c == nil || c.Store == nil || c.Inner == nil {
		return Classification{}, fmt.Errorf("decisions: store cache needs a store and an inner classifier")
	}
	m := Meta(item)
	if m == nil || m.Source == "" || m.SourceID == "" {
		return c.Inner.Classify(ctx, item)
	}
	if cached, ok, err := c.Store.GetDecisionClassification(ctx, m.Source, m.SourceID); err != nil {
		return Classification{}, fmt.Errorf("decisions: reading cached classification: %w", err)
	} else if ok {
		return Classification{NeedsDecision: cached.NeedsDecision, TypeID: cached.TypeID, Confidence: cached.Confidence}, nil
	}
	result, err := c.Inner.Classify(ctx, item)
	if err != nil {
		return Classification{}, err
	}
	if result.Fallback {
		// An unreadable reply is not a verdict: don't pin the item to
		// generic forever (the Triager in front bounds how often it retries).
		return result, nil
	}
	if err := c.Store.SetDecisionClassification(ctx, store.DecisionClassification{
		Source: m.Source, SourceID: m.SourceID,
		NeedsDecision: result.NeedsDecision, TypeID: result.TypeID, Confidence: result.Confidence,
		ClassifiedAt: time.Now(),
	}); err != nil {
		return Classification{}, fmt.Errorf("decisions: caching classification: %w", err)
	}
	return result, nil
}

// Forget deletes ref's ("source:source_id") persisted verdict, so the next
// Classify asks Inner again. It implements Forgetter.
func (c *StoreCache) Forget(ctx context.Context, ref string) error {
	if c == nil || c.Store == nil {
		return nil
	}
	source, id, ok := strings.Cut(ref, ":")
	if !ok || source == "" || id == "" {
		return fmt.Errorf("decisions: forget: %q is not a source:source_id ref", ref)
	}
	return c.Store.DeleteDecisionClassification(ctx, source, id)
}

// DefaultWindow is how far back Trigger.Run looks for candidate messages
// when Window is unset. It only bounds the store scan: an item already
// classified (in either cache) costs nothing extra beyond the scan itself,
// so this can be generous without repeating model calls.
const DefaultWindow = 7 * 24 * time.Hour

// Trigger is the reusable orchestration piece: given a store to scan, a
// Triager (which already carries the candidate predicate and a Classifier —
// typically a StoreCache wrapping a ModelClassifier) and a Builder, it
// finds candidate messages, classifies the ones not already classified, and
// builds a Card for every one that needs a decision. Wiring this into the
// daemon's sync tick or the CLI is a separate task; Trigger only needs a
// *store.Store, a *Triager and a *Builder to run standalone in a test.
type Trigger struct {
	Store   *store.Store
	Triager *Triager
	Builder *Builder
	// Window bounds how far back Run looks ("Since" on store.Query). Zero
	// means DefaultWindow.
	Window time.Duration
}

// Report is one RunReport pass: the cards that built, and one error per
// candidate item that could not be classified or built this time (a
// usage-cap refusal, a backend outage). A skipped item is retried on the
// next run; it never takes the other items' cards down with it.
type Report struct {
	Cards   []*Card
	Skipped []error
}

// Run scans messages created since now-Window and, for each the Triager's
// candidate predicate flags, classifies it (a no-op against either cache
// once already classified) and builds a Card when the classification says a
// decision is needed. It returns the cards built on this call; nothing here
// persists cards themselves (only the classification verdict is cached, so
// a rebuilt card always reflects current gate/connector state).
//
// A per-item failure is skipped, not fatal: the cards that did build are
// still returned (use RunReport to see what was skipped). Only failing to
// list the store at all is an error.
func (tr *Trigger) Run(ctx context.Context, now time.Time) ([]*Card, error) {
	rep, err := tr.RunReport(ctx, now)
	return rep.Cards, err
}

// RunReport is Run, also reporting each skipped item.
func (tr *Trigger) RunReport(ctx context.Context, now time.Time) (Report, error) {
	if tr == nil || tr.Store == nil || tr.Triager == nil || tr.Builder == nil {
		return Report{}, fmt.Errorf("decisions: trigger needs a store, a triager and a builder")
	}
	window := tr.Window
	if window <= 0 {
		window = DefaultWindow
	}
	msgs, err := store.List[store.Message, *store.Message](ctx, tr.Store, store.Query{Since: now.Add(-window)})
	if err != nil {
		return Report{}, fmt.Errorf("decisions: trigger: listing messages: %w", err)
	}
	var rep Report
	for i := range msgs {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		item := &msgs[i]
		c, ok, err := tr.Triager.Triage(ctx, item)
		if err != nil {
			rep.Skipped = append(rep.Skipped, fmt.Errorf("decisions: trigger: classifying %s: %w", Ref(item), err))
			continue
		}
		if !ok || !c.NeedsDecision {
			continue
		}
		card, err := tr.Builder.Build(ctx, item, c)
		if err != nil {
			rep.Skipped = append(rep.Skipped, fmt.Errorf("decisions: trigger: building card for %s: %w", Ref(item), err))
			continue
		}
		rep.Cards = append(rep.Cards, card)
	}
	return rep, nil
}
