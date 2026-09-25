// Package t1 is the Tier 1 (FunctionGemma) sidecar client: it turns the
// intent registry's reflex-eligible read and write intents into
// llama-server / OpenAI-style tool declarations, sends one
// /v1/chat/completions request per turn with those declarations, and parses
// the reply into at most one proposed Call. See docs/slices/R.md §10 and
// docs/functiongemma.md.
package t1

import (
	"sort"
	"strings"

	"water/internal/nervous/intents"
)

// ParamDecl is one declared parameter of a tool. Every slot Tier 1 sees is
// typed as a plain string on the wire: the model states the literal
// utterance-derived value, and the Tier 1 adapter (internal/nervous's
// TryTier1) resolves that string into a real type with the same slots
// package Tier 0 uses (slots.ResolveString), then grounds it against the
// utterance before ever trusting it.
type ParamDecl struct {
	Type        string
	Description string
	Required    bool
}

// Decl is one intent's tool declaration, in the shape llama-server's
// OpenAI-compatible /v1/chat/completions endpoint expects for "tools".
//
// Name is a wire-safe identifier: OpenAI-style tool names may not contain a
// dot, so Name replaces an intent id's single "." with "_"
// ("mail.latest_from" -> "mail_latest_from"). intentID keeps the original
// registry id alongside it (an unexported field, not part of the
// {Name,Description,Params} shape a caller builds Decl values with) so a
// Client can recover the real intent id from a reply's tool name without
// having to reverse that transform — which would be ambiguous for a
// hypothetical future intent id whose first segment itself contained an
// underscore. Every Decl exists only via Declarations, which sets this
// field itself.
type Decl struct {
	Name        string
	Description string
	Params      map[string]ParamDecl

	intentID string
}

// IntentID returns the registry intent id this declaration was built from.
func (d Decl) IntentID() string { return d.intentID }

// declName converts an intent id ("mail.latest_from") into a wire-safe tool
// name ("mail_latest_from") by replacing its single "." with "_".
func declName(id string) string {
	return strings.Replace(id, ".", "_", 1)
}

// Declarations returns one Decl per intent Tier 1 may ever propose:
// reflex_eligible (the same flag that gates a read intent's Tier-0 handler
// and a write intent's proposal) and not requires_pending_approval —
// approvals.respond is answered directly from the pending queue's own
// yes/no vocabulary (Tier 0's bindPendingHandler), never from a model's
// guess at intent, so it is never declared to FunctionGemma at all. Both
// read and write kinds are included: a write intent Tier 1 matches still
// only ever produces a proposal, never an executed action.
func Declarations(r *intents.Registry) []Decl {
	var out []Decl
	for _, it := range r.Candidates() {
		if !it.ReflexEligible || it.RequiresPending {
			continue
		}
		params := make(map[string]ParamDecl, len(it.Slots))
		for name, spec := range it.Slots {
			params[name] = ParamDecl{
				Type:        "string",
				Description: string(spec.Type),
				Required:    spec.Required,
			}
		}
		out = append(out, Decl{
			Name:        declName(it.ID),
			Description: it.Description,
			Params:      params,
			intentID:    it.ID,
		})
	}
	// Deterministic order: reproducible request bodies (useful for tests and
	// for comparing two eval runs byte-for-byte) and no dependency on map
	// iteration order.
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
