package decider

import (
	"context"
	"errors"
	"fmt"

	"water/internal/decisions"
	"water/internal/store"
)

// Classifier adapts a Decider to decisions.Classifier: it asks D to choose
// one of Registry's own registered type ids (plus generic) for an item, and
// falls back to Fallback's own Classify whenever D declines
// (ErrUnavailable) or fails for any other reason. It never fails a
// classification outright just because D had nothing to say.
type Classifier struct {
	D        Decider
	Fallback decisions.Classifier
	Registry *decisions.Registry
}

// Wrap returns a decisions.Classifier that asks d before falling back to
// fallback. When d is nil or a Null{}, Wrap returns fallback UNCHANGED —
// literally the same value, not even wrapped — so the default path (no
// decider configured) adds zero indirection: d.Decide is never referenced,
// let alone called, and fallback's own Classify runs exactly as it would
// have with no decider package involved at all.
func Wrap(d Decider, fallback decisions.Classifier, reg *decisions.Registry) decisions.Classifier {
	if isNull(d) {
		return fallback
	}
	return &Classifier{D: d, Fallback: fallback, Registry: reg}
}

// isNull reports whether d is exactly Null{} (by value) or nil — the two
// shapes Wrap treats as "no decider configured".
func isNull(d Decider) bool {
	if d == nil {
		return true
	}
	_, ok := d.(Null)
	return ok
}

// Classify implements decisions.Classifier.
func (c *Classifier) Classify(ctx context.Context, item store.Record) (decisions.Classification, error) {
	if c == nil || c.Fallback == nil {
		return decisions.Classification{}, errors.New("decider: classifier needs a fallback")
	}
	options := c.options()
	ans, err := c.D.Decide(ctx, Request{Kind: Choice, State: stateText(item), Options: options})
	if err != nil {
		// ErrUnavailable (no provider) or any other failure: never fail the
		// classification outright, always defer to the classifier that ran
		// before this package existed.
		return c.Fallback.Classify(ctx, item)
	}
	typeID := ans.Choice
	if c.Registry != nil {
		if _, ok := c.Registry.Lookup(typeID); !ok {
			typeID = decisions.GenericID
		}
	} else if typeID == "" {
		typeID = decisions.GenericID
	}
	return decisions.Classification{NeedsDecision: true, TypeID: typeID, Confidence: ans.Confidence}, nil
}

// options is the registry's own registered type ids, plus generic — the
// same closed set decisions.ModelClassifier offers a model today.
func (c *Classifier) options() []string {
	var out []string
	if c.Registry != nil {
		for _, e := range c.Registry.Index() {
			out = append(out, e.ID)
		}
	}
	return append(out, decisions.GenericID)
}

// stateText renders item as the plain-text state a Decider sees. It
// mirrors decisions' own item rendering (classify.go's itemText, record.go's
// describe) using only exported store fields: this package must not import
// internal/decisions' unexported helpers, so this is a small, deliberate,
// documented duplication of the same shape, not a second implementation of
// any gated logic — Decide never runs a connector call or touches the gate.
func stateText(item store.Record) string {
	switch v := item.(type) {
	case *store.Message:
		return fmt.Sprintf("From: %s\nSubject: %s\n%s", v.From, v.Subject, v.Body)
	case *store.Document:
		return fmt.Sprintf("Document: %s (owner %s)\n%s", v.Title, v.Owner, v.Excerpt)
	default:
		if item == nil {
			return ""
		}
		return item.Table()
	}
}
