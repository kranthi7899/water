package nervous

import (
	"context"
	"errors"
	"time"

	"water/internal/nervous/intents"
	"water/internal/nervous/reflex"
	"water/internal/nervous/render"
	"water/internal/nervous/slots"
	"water/internal/nervous/t1"
	"water/internal/nervous/tmpl"
)

// countWords mirrors internal/nervous/slots's own number-word vocabulary
// (1..20, internal/nervous/slots/count.go's numberWords), duplicated here
// rather than exported from slots, so a count argument's grounding check
// (below) can accept either form the model might report ("3" or "three")
// without slots needing to expose its parsing internals for this one
// cross-package need.
var countWords = map[string]string{
	"1": "one", "2": "two", "3": "three", "4": "four", "5": "five",
	"6": "six", "7": "seven", "8": "eight", "9": "nine", "10": "ten",
	"11": "eleven", "12": "twelve", "13": "thirteen", "14": "fourteen", "15": "fifteen",
	"16": "sixteen", "17": "seventeen", "18": "eighteen", "19": "nineteen", "20": "twenty",
}

// countEquivalent returns tok's other accepted spelling (digit -> word or
// word -> digit) for grounding purposes, per countWords above.
func countEquivalent(tok string) (string, bool) {
	if word, ok := countWords[tok]; ok {
		return word, true
	}
	for digit, word := range countWords {
		if word == tok {
			return digit, true
		}
	}
	return "", false
}

// TryTier1 is the sous chef's second pass: ask FunctionGemma, over the
// sidecar client, to propose exactly one intent and its string arguments
// for u, validate that proposal through the SAME Validate Tier 0 uses, and
// only then apply the two checks that are Tier 1-only — grounding and the
// deny-word check — before ever running a handler.
//
// The caller (Nervous.Handle) is responsible for calling TryTier1 only when
// Tier 0 itself escalated with reason "no_match" (Design §10: "never for
// reasons like escalate_word", and never at all when eligibility itself
// already rejected the turn) — TryTier1 does not re-check eligibility or
// Tier 0's own escalation reason itself, so its own tests can exercise it
// directly without re-deriving that gate.
//
// A nil result with a non-empty escalationReason (and nil err) means
// "escalate, Tier 1 has no answer": "t1_no_call", "t1_multi_call",
// "t1_text", "t1_unknown_intent", "t1_ungrounded", "action_word",
// "slot_unresolved" or "ambiguous_match" (the latter two from Validate,
// exactly as Tier 0 reports them). A non-nil result means Tier 1 answered.
// err is only ever a transport/protocol failure talking to the sidecar, or
// the matched handler's own error.
func TryTier1(ctx context.Context, reg *intents.Registry, deps reflex.Deps, client t1.Client, u tmpl.Utterance, now time.Time, ents slots.Entities) (*render.Result, string, error) {
	decls := t1.Declarations(reg)
	calls, err := client.Propose(ctx, u.Raw, decls)
	if errors.Is(err, t1.ErrTextReply) {
		return nil, "t1_text", nil
	}
	if err != nil {
		return nil, "", err
	}
	switch {
	case len(calls) == 0:
		return nil, "t1_no_call", nil
	case len(calls) > 1:
		return nil, "t1_multi_call", nil
	}
	call := calls[0]

	it, ok := candidateByID(reg, call.Intent)
	if !ok || !it.ReflexEligible || it.RequiresPending {
		// Not a real, currently-declared Tier 1 target: an unrecognized
		// name, an intent that lost its reflex_eligible flag or went
		// inactive since Declarations was built, or (a fake/misbehaving
		// client only) approvals.respond, which Declarations never offers.
		return nil, "t1_unknown_intent", nil
	}

	// Build one tmpl.Capture per argument the model actually supplied,
	// exactly the way slots.ResolveString builds one for a single string
	// (normalize for typed resolution, keep the original as Raw for a text
	// slot) — so Validate resolves Tier 1's string arguments through the
	// identical typed-slot logic Tier 0's template captures go through.
	captures := make([]tmpl.Capture, 0, len(call.Args))
	for name, val := range call.Args {
		n := tmpl.Normalize(val, nil)
		captures = append(captures, tmpl.Capture{Slot: name, Tokens: n.Tokens, Raw: val})
	}

	v, reason, ok := Validate(reg, Proposal{Intent: call.Intent, Captures: captures}, now, ents)
	if !ok {
		return nil, reason, nil
	}

	if ungroundedSlot(v, call.Args, u) {
		return nil, "t1_ungrounded", nil
	}
	if denyWordOutsideVocabulary(reg, u, it) {
		return nil, "action_word", nil
	}

	handler, ok := reflex.Table()[it.Function]
	if !ok {
		// Same drift-guard as TryTier0: the registry loader already checks
		// every read intent's function exists in reflex.Specs() at load
		// time, so reaching here would mean the handler table and the
		// registry's function set have drifted.
		return nil, "t1_unknown_intent", nil
	}
	result, err := handler.Run(ctx, deps, v.Args)
	if err != nil {
		return nil, "", err
	}
	return &result, "", nil
}

// ungroundedSlot reports whether any argument the model itself supplied
// (providedArgs — never a slot Validate filled in from the intent's own
// default, since those are exempt by construction: this function never
// looks at v.Args for a name providedArgs doesn't contain) fails to appear
// as a contiguous, normalized token run in u. A count slot may ground on
// either its digit or its number-word spelling.
func ungroundedSlot(v Validated, providedArgs map[string]string, u tmpl.Utterance) bool {
	for name, raw := range providedArgs {
		spec, declared := v.Intent.Slots[name]
		if !declared {
			// Validate already fails a proposal with an undeclared slot
			// name before returning ok=true, so this can't happen for a v
			// this function is ever called with; skip rather than panic.
			continue
		}
		n := tmpl.Normalize(raw, nil)
		if len(n.Tokens) == 0 {
			return true
		}
		if containsSubsequence(u.Tokens, n.Tokens) {
			continue
		}
		if spec.Type == slots.TypeCount && len(n.Tokens) == 1 {
			if alt, ok := countEquivalent(n.Tokens[0]); ok && containsSubsequence(u.Tokens, []string{alt}) {
				continue
			}
		}
		return true
	}
	return false
}

// denyWordOutsideVocabulary reports whether u contains a configured deny
// word that is not part of it's own compiled templates' literal vocabulary
// (the union across every one of it's templates — Tier 1 has no single
// matched template of its own to consult, unlike Tier 0's per-match
// LiteralSet, so the exemption is the intent's full literal vocabulary
// instead).
func denyWordOutsideVocabulary(reg *intents.Registry, u tmpl.Utterance, it intents.Intent) bool {
	deny := wordSet(reg.Shared().DenyWords)
	if len(deny) == 0 {
		return false
	}
	vocab := map[string]bool{}
	for _, tpl := range reg.Templates(it.ID) {
		for w := range tpl.LiteralVocabulary() {
			vocab[w] = true
		}
	}
	for _, tok := range u.Tokens {
		if deny[tok] && !vocab[tok] {
			return true
		}
	}
	return false
}
