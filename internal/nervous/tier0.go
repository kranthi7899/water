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
	labels   map[string]string
}

// matchOnly runs Tier 0's deterministic template match and validation
// against every live candidate (Design §11.4 step 3, minus the final
// step of actually running the winning handler): match u against every
// live intent's templates, validate whatever matches through the same
// Validate every quick tier uses, and pick the single most specific
// validated match. It touches no store, no backend and no model client —
// pure with respect to anything but reg/u/pending/now/ents — so both
// TryTier0 (which then runs the winning handler) and speculate.go's dry
// match (Design §11.5, which never runs anything through this package's own
// say-so) can share the exact same matching logic instead of either one
// reimplementing the loop.
//
// A false ok with a non-empty escalationReason means "no committed match" —
// one of "no_match", "action_word", "slot_unresolved" or "ambiguous_match".
// A true ok means exactly one most-specific candidate won.
func matchOnly(reg *intents.Registry, u tmpl.Utterance, pending int, now time.Time, ents slots.Entities) (tier0Match, string, bool) {
	shared := reg.Shared()
	denyWords := wordSet(shared.DenyWords)

	var matches []tier0Match
	sawDenyReject := false
	sawSlotReject := false

	for _, it := range reg.Candidates() {
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
				matches = append(matches, tier0Match{intent: v.Intent, literals: m.Literals, args: v.Args, labels: v.Labels})
				return false // keep enumerating: a later, more specific match (or a competing intent) may still turn up
			})
		}
	}

	if len(matches) == 0 {
		switch {
		case sawDenyReject:
			return tier0Match{}, "action_word", false
		case sawSlotReject:
			return tier0Match{}, "slot_unresolved", false
		default:
			return tier0Match{}, "no_match", false
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
		return tier0Match{}, "ambiguous_match", false
	}
	return top, "", true
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
func TryTier0(ctx context.Context, reg *intents.Registry, deps reflex.Deps, u tmpl.Utterance, pending int, now time.Time, ents slots.Entities, wh writeHandler) (*render.Result, string, error) {
	top, escReason, ok := matchOnly(reg, u, pending, now, ents)
	if !ok {
		return nil, escReason, nil
	}
	return runTier0Match(ctx, deps, top, wh)
}

// runTier0Match runs the winning candidate's real reflex handler, or, for a
// write-kind candidate, hands it to wh (Design §12) instead of ever looking
// it up in reflex.Table() (a write intent's Function is always empty --
// LoadRegistry itself enforces that). Split out from TryTier0 so a caller
// that already has a matchOnly result (Handle's own speculation-reuse
// check, tier0.go's own TryTier0 above) never needs a second copy of "look
// the handler up and run it."
func runTier0Match(ctx context.Context, deps reflex.Deps, top tier0Match, wh writeHandler) (*render.Result, string, error) {
	if top.intent.Kind == intents.KindWrite {
		if wh == nil {
			return nil, "no_match", nil
		}
		return wh(ctx, top.intent, top.args)
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
