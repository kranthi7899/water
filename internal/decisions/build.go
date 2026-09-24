package decisions

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
)

// Invoker is the gate's call surface; *gate.Gate implements it.
type Invoker interface {
	Invoke(ctx context.Context, c gate.Call) (gate.Result, error)
}

// Outcome is how one need resolved.
type Outcome string

const (
	OutcomeFound Outcome = "found"
	OutcomeEmpty Outcome = "empty"
	OutcomeError Outcome = "error"
)

// NeedResult is one resolved need. Records holds a connector fetch's
// normalized records (the item itself excluded); Compute holds an
// internal:// result.
type NeedResult struct {
	Need     Need
	Outcome  Outcome
	Records  []store.Record
	Compute  ComputeResult
	External bool
	Err      error
	// Note explains an empty outcome that was not a search (e.g. the item
	// had nothing to search on).
	Note string
}

// ReadinessOf is the readiness rule, a pure function of the fetch
// outcomes: blocked if any fetch errored, missing_info if any came back
// empty, otherwise ready. A type with no needs is ready.
func ReadinessOf(results []NeedResult) Readiness {
	r := Ready
	for _, res := range results {
		switch res.Outcome {
		case OutcomeError:
			return Blocked
		case OutcomeEmpty:
			r = MissingInfo
		}
	}
	return r
}

// Builder prepares cards. Gate and Registry are required; Phraser is
// optional (nil keeps the code-built prose). Origin is the gate origin
// every fetch is made under; the zero value means P1, per C.md (card
// preparation reacts to something the CEO or a sync tick surfaced).
type Builder struct {
	Registry *Registry
	Gate     Invoker
	Phraser  Phraser
	Origin   gate.Origin
}

// Build prepares a card for item as type c.TypeID (generic when that id is
// unknown). It never drops the item: a failing fetch or an unsourced
// figure lowers readiness and becomes a stated gap. An error is returned
// only for a nil item or a misconfigured builder.
func (b *Builder) Build(ctx context.Context, item store.Record, c Classification) (*Card, error) {
	if b == nil || b.Registry == nil || b.Gate == nil {
		return nil, errors.New("decisions: builder needs a registry and a gate")
	}
	if item == nil || Meta(item) == nil {
		return nil, errors.New("decisions: build needs a store record")
	}
	t, ok := b.Registry.Lookup(c.TypeID)
	if !ok {
		t = b.Registry.Generic()
	}
	ref := Ref(item)
	card := &Card{
		ID:             cardID(t.ID, ref),
		TypeID:         t.ID,
		Severity:       t.SeverityWeight,
		Defaults:       map[string]any{},
		DefaultSources: map[string]string{},
		Parameters:     map[string]any{},
		Untrusted:      external(item),
	}
	if ref != "" {
		card.SourceItemIDs = []string{ref}
		card.Evidence = append(card.Evidence, Evidence{Text: describe(item), Source: ref})
	}
	traced := map[string]bool{ref: ref != ""}

	results := b.resolve(ctx, t, item)
	for _, res := range results {
		card.Untrusted = card.Untrusted || res.External
		switch res.Outcome {
		case OutcomeError:
			card.Gaps = append(card.Gaps, fmt.Sprintf("Couldn't get %s: %s.", humanize(res.Need.Name), clip(res.Err.Error(), 160)))
		case OutcomeEmpty:
			if res.Note != "" {
				card.Gaps = append(card.Gaps, fmt.Sprintf("Couldn't look for %s: %s.", humanize(res.Need.Name), res.Note))
			} else {
				card.Gaps = append(card.Gaps, fmt.Sprintf("Nothing found for %s.", humanize(res.Need.Name)))
			}
		}
		for _, r := range res.Records {
			rr := Ref(r)
			traced[rr] = rr != ""
			card.Evidence = append(card.Evidence, Evidence{Text: describe(r), Source: rr})
		}
		card.Evidence = append(card.Evidence, res.Compute.Evidence...)
		for _, k := range sortedKeys(res.Compute.Figures) {
			f := res.Compute.Figures[k]
			if _, dup := card.Defaults[k]; dup {
				card.Gaps = append(card.Gaps, fmt.Sprintf("The figure %q was computed twice; neither value is shown.", k))
				delete(card.Defaults, k)
				delete(card.DefaultSources, k)
				continue
			}
			card.Defaults[k], card.DefaultSources[k] = f.Value, f.Source
		}
	}
	card.Readiness = ReadinessOf(results)

	// A source must trace to this build: the item, a fetched record, or
	// named code. Anything else is treated as unsourced.
	for i, e := range card.Evidence {
		if !traceable(e.Source, traced) {
			card.Evidence[i].Source = ""
		}
	}
	for k, s := range card.DefaultSources {
		if !traceable(s, traced) {
			card.DefaultSources[k] = ""
		}
	}
	if card.Validate() != nil {
		card.quarantine()
	}
	for k, v := range card.Defaults {
		card.Parameters[k] = v
	}

	for _, a := range t.StagedActions {
		card.StagedActions = append(card.StagedActions, StagedAction{Function: a, Actionable: actionable(b.Registry.Manifest(), a)})
	}

	b.codeProse(card, t, item)
	if b.Phraser != nil {
		if p, err := b.Phraser.Phrase(ctx, *card, t); err == nil {
			applyProse(card, p)
		}
	}
	if err := card.Validate(); err != nil {
		// Unreachable unless a phraser mutated sources; fail closed anyway.
		card.quarantine()
	}
	return card, nil
}

// resolve runs every need in order. Connector fetches go through the gate
// at the need's level (R, enforced at load); internal:// needs call the
// registered Go function with everything resolved before it.
func (b *Builder) resolve(ctx context.Context, t Type, item store.Record) []NeedResult {
	origin := b.Origin
	if origin == "" {
		origin = gate.P1
	}
	f := fields(item)
	self := Ref(item)
	resolved := map[string]NeedResult{}
	var out []NeedResult
	for _, n := range t.Needs {
		res := NeedResult{Need: n}
		if n.Internal() {
			res = runCompute(ctx, n, item, resolved)
		} else {
			args, missing := fill(n.Args, f)
			switch {
			case missing != "":
				res.Outcome, res.Note = OutcomeEmpty, "the item has no "+humanize(missing)+" to search on"
			default:
				taint := gate.Clean
				if usesItem(n.Args) && external(item) {
					taint = gate.Tainted
				}
				r, err := b.Gate.Invoke(ctx, gate.Call{Function: n.Fetch, Args: args, Origin: origin, Taint: taint})
				switch {
				case errors.Is(err, store.ErrNotFound):
					res.Outcome = OutcomeEmpty
				case err != nil:
					res.Outcome, res.Err = OutcomeError, err
				default:
					for _, rec := range r.Records {
						if rec == nil || Meta(rec) == nil || (self != "" && Ref(rec) == self) {
							continue
						}
						res.External = res.External || r.Untrusted || external(rec)
						res.Records = append(res.Records, rec)
					}
					res.Outcome = OutcomeFound
					if len(res.Records) == 0 {
						res.Outcome = OutcomeEmpty
					}
				}
			}
		}
		resolved[n.Name] = res
		out = append(out, res)
	}
	return out
}

func runCompute(ctx context.Context, n Need, item store.Record, resolved map[string]NeedResult) (res NeedResult) {
	res = NeedResult{Need: n}
	fn, ok := lookupCompute(n.Fetch)
	if !ok {
		res.Outcome, res.Err = OutcomeError, fmt.Errorf("no compute function %s", n.Fetch)
		return res
	}
	in := ComputeInput{Item: item, Resolved: map[string]NeedResult{}}
	for k, v := range resolved {
		in.Resolved[k] = v
	}
	defer func() {
		if p := recover(); p != nil {
			res = NeedResult{Need: n, Outcome: OutcomeError, Err: fmt.Errorf("%s panicked: %v", n.Fetch, p)}
		}
	}()
	out, err := fn(ctx, in)
	if err != nil {
		res.Outcome, res.Err = OutcomeError, err
		return res
	}
	res.Compute, res.External = out, out.External
	res.Outcome = OutcomeFound
	if out.empty() {
		res.Outcome = OutcomeEmpty
	}
	return res
}

// fill substitutes placeholders. It reports the first placeholder whose
// value is empty: a query built from nothing would search for everything.
func fill(tmpl map[string]string, f map[string]string) (map[string]any, string) {
	args := map[string]any{}
	for k, v := range tmpl {
		var missing string
		s := placeholderRe.ReplaceAllStringFunc(v, func(m string) string {
			name := m[1 : len(m)-1]
			if f[name] == "" && missing == "" {
				missing = name
			}
			return f[name]
		})
		if missing != "" {
			return nil, missing
		}
		args[k] = s
	}
	return args, ""
}

func usesItem(tmpl map[string]string) bool {
	for _, v := range tmpl {
		if placeholderRe.MatchString(v) {
			return true
		}
	}
	return false
}

func traceable(src string, traced map[string]bool) bool {
	if strings.HasPrefix(src, "code:") && len(src) > len("code:") {
		return true
	}
	return traced[src]
}

func actionable(m *twins.Manifest, fn string) bool {
	if m == nil {
		return false
	}
	f, ok := m.Function(fn)
	return ok && f.Level == twins.A
}

func cardID(typeID, ref string) string {
	sum := sha256.Sum256([]byte(typeID + "\x00" + ref))
	return "card-" + hex.EncodeToString(sum[:])[:16]
}

func humanize(s string) string { return strings.ReplaceAll(s, "_", " ") }

// codeProse fills the prose fields from the item and type alone, so a card
// is complete and truthful without any model.
func (b *Builder) codeProse(c *Card, t Type, item store.Record) {
	f := fields(item)
	subject := f["subject"]
	if subject == "" {
		subject = "an item with no subject"
	}
	lead := t.Title + ": " + subject
	if who := f["sender"]; who != "" {
		lead += " (from " + who + ")"
	}
	c.Lead = clip(lead, 160)
	if t.ID == GenericID {
		c.Question = "What exactly is being decided here, and by when?"
		c.Gaps = append(c.Gaps, genericQuestions...)
	} else {
		c.Question = clip(t.Title+": decide on "+subject+"?", 200)
	}
	if c.Deadline == nil {
		c.Gaps = append(c.Gaps, "No deadline was found.")
	}
}
