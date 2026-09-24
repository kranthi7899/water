package nervous

import (
	"context"
	"time"

	"water/internal/nervous/intents"
	"water/internal/nervous/reflex"
	"water/internal/nervous/render"
	"water/internal/nervous/slots"
	"water/internal/nervous/tmpl"
)

// tier0Match is one candidate answer that survived the deny-word check and
// validated cleanly, kept around long enough to compare specificity across
// every candidate before committing to one.
type tier0Match struct {
	intent   intents.Intent
	literals int
	args     reflex.Args
}

// TryTier0 is the sous chef's deterministic first pass (Design §11.4 step
// 3): match u against every live intent's templates, validate whatever
// matches, and run the single most specific validated match's handler. It
// takes no backend or model client at all, so a Tier 0 attempt can never
// make a model call by construction.
//
// A nil result with a non-empty escalationReason (and nil err) means
// "escalate, Tier 0 has no answer" — one of "no_match", "action_word",
// "slot_unresolved" or "ambiguous_match". A non-nil result means Tier 0
// answered. err is only ever the matched handler's own error.
func TryTier0(ctx context.Context, reg *intents.Registry, deps reflex.Deps, u tmpl.Utterance, pending int, now time.Time, ents slots.Entities) (*render.Result, string, error) {
	shared := reg.Shared()
	denyWords := wordSet(shared.DenyWords)

	var matches []tier0Match
	sawDenyReject := false
	sawSlotReject := false

	for _, it := range reg.Candidates() {
		if it.Kind == intents.KindWrite {
			// Write-intent proposals (create/move an event, draft or send
			// a reply) go through a separate proposal path a later task
			// builds; Tier 0 as built here only ever answers reads.
			continue
		}
		if it.RequiresPending && pending <= 0 {
			continue
		}

		for _, tpl := range reg.Templates(it.ID) {
			tpl.MatchAll(u, func(m tmpl.Match) bool {
				if denyWordHit(u.Tokens, denyWords, m.LiteralSet) {
					sawDenyReject = true
					return false
				}
				v, reason, ok := Validate(reg, Proposal{Intent: it.ID, Captures: m.Captures}, now, ents)
				if !ok {
					if reason == "slot_unresolved" {
						sawSlotReject = true
					}
					return false
				}
				matches = append(matches, tier0Match{intent: v.Intent, literals: m.Literals, args: v.Args})
				return false // keep enumerating: a later, more specific match (or a competing intent) may still turn up
			})
		}
	}

	if len(matches) == 0 {
		switch {
		case sawDenyReject:
			return nil, "action_word", nil
		case sawSlotReject:
			return nil, "slot_unresolved", nil
		default:
			return nil, "no_match", nil
		}
	}

	top := matches[0]
	tie := false
	for _, m := range matches[1:] {
		switch {
		case m.literals > top.literals:
			top, tie = m, false
		case m.literals == top.literals && m.intent.ID != top.intent.ID:
			tie = true
		}
	}
	if tie {
		return nil, "ambiguous_match", nil
	}

	handler, ok := reflex.Table()[top.intent.Function]
	if !ok {
		// The registry loader already checks every read intent's function
		// exists in reflex.Specs() at load time, so this would mean the
		// handler table and the registry's function set have drifted.
		return nil, "no_match", nil
	}
	result, err := handler.Run(ctx, deps, top.args)
	if err != nil {
		return nil, "", err
	}
	return &result, "", nil
}

// denyWordHit reports whether any token the utterance contains is a
// configured deny word that this particular match did not itself consume
// as a template literal (a literal like control.stop's own "cancel" is
// exempt for that intent, since the match's LiteralSet names exactly the
// literal tokens it consumed).
func denyWordHit(tokens []string, denyWords map[string]bool, literalSet []string) bool {
	if len(denyWords) == 0 {
		return false
	}
	consumed := wordSet(literalSet)
	for _, tok := range tokens {
		if denyWords[tok] && !consumed[tok] {
			return true
		}
	}
	return false
}
