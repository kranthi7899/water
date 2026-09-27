package gateway

import (
	"context"
	"regexp"
	"strings"
	"time"

	"water/internal/approvals"
	"water/internal/decisions"
	"water/internal/store"
)

// gistMaxLen bounds Gist's fallback truncation for a body with no
// sentence-ending punctuation at all (docs/slices/UI.md Phase 3a's
// agent-draft card: "the first sentence of the body, code-cut").
const gistMaxLen = 140

// sentenceEnd matches the first '.', '!' or '?' that is immediately
// followed by whitespace or the end of the string -- Phase 3a's own rule
// for where a "one-sentence gist" ends ("split on the first ./!/? followed
// by whitespace-or-end"). A period inside something like "10.5" or a URL is
// not followed by whitespace, so it is never mistaken for a sentence end;
// this is a deliberately simple, literal rule, not real sentence detection.
var sentenceEnd = regexp.MustCompile(`[.!?](\s|$)`)

// Gist is the Approvals queue's one-sentence summary of a mail-shaped
// envelope's body: everything up to and including the first sentence-ending
// punctuation mark (sentenceEnd), trimmed. A body with no such punctuation
// falls back to a plain truncation at gistMaxLen runes, cut back to the last
// whole word and marked with "…" so it never reads as a complete sentence it
// isn't. "" for an empty or all-whitespace body.
func Gist(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	if loc := sentenceEnd.FindStringIndex(body); loc != nil {
		return strings.TrimSpace(body[:loc[0]+1])
	}
	r := []rune(body)
	if len(r) <= gistMaxLen {
		return body
	}
	cut := string(r[:gistMaxLen])
	if i := strings.LastIndex(cut, " "); i > gistMaxLen/2 {
		cut = cut[:i]
	}
	return strings.TrimSpace(cut) + "…"
}

// riskPhrases is Phase 3a's code table from a connector risk level
// (connectors.Risk, as carried on Envelope.Risk) to a plain-language phrase
// for the Approvals queue's dense rows, e.g. "Medium risk · external
// recipient, new commitment". The reason clause after "·" is a fixed,
// generic phrase per level, not computed from the specific envelope's
// payload -- Phase 3a's own instruction is to keep this simple rather than
// infer a bespoke reason, so the table is the whole design.
var riskPhrases = map[string]string{
	"low":    "Low risk · reversible or internal-only",
	"medium": "Medium risk · external recipient, new commitment",
	"high":   "High risk · sends money, signs, or can't be undone",
}

// RiskPhrase renders risk (an Envelope.Risk value) in words via riskPhrases.
// An empty or unrecognized value (an envelope from before risk
// classification existed, or a connector with no risk tag) reads as "Risk
// not rated" rather than a blank line.
func RiskPhrase(risk string) string {
	if p, ok := riskPhrases[risk]; ok {
		return p
	}
	return "Risk not rated"
}

// Initials computes a two-letter initials avatar from a resolved roster
// display name (docs/slices/UI.md Phase 3a's person-request card). Two or
// more words: the first letter of the first and last word ("Lee Chen" ->
// "LC"). One word: its first two letters, or its only letter if it has just
// one ("Cher" -> "CH", "X" -> "X"). Empty (or all-whitespace) name: "?" --
// a visible placeholder rather than a misleadingly blank avatar.
func Initials(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "?"
	}
	fields := strings.Fields(name)
	if len(fields) == 1 {
		r := []rune(fields[0])
		if len(r) == 1 {
			return strings.ToUpper(string(r))
		}
		return strings.ToUpper(string(r[:2]))
	}
	first := []rune(fields[0])
	last := []rune(fields[len(fields)-1])
	return strings.ToUpper(string(first[0]) + string(last[0]))
}

// cardTitleOf is a decision card's own display title (Lead, falling back to
// Question) -- the same choice needsyou.cardTitle makes, so an approval's
// "From <decision>" line never disagrees with what Today or the Decisions
// view calls the same card.
func cardTitleOf(c *decisions.Card) string {
	if c.Lead != "" {
		return c.Lead
	}
	return c.Question
}

// sourceCardTitles resolves every decision card currently known (live
// classifier plus persisted decision_records, exactly as GET /v1/decisions
// merges them) into an id -> title map, for a batch of envelopes'
// SourceCardID lookups. A live-classifier failure is swallowed here (best
// effort: a missing title just means the card's "From <decision>" line is
// omitted, never a failed Approvals list).
func (d *Daemon) sourceCardTitles(ctx context.Context) map[string]string {
	var computed []*decisions.Card
	if d.cfg.Decisions != nil {
		computed, _ = d.cfg.Decisions.Run(ctx, time.Now())
	}
	cards, err := decisions.MergeFromStore(ctx, d.cfg.Store, computed)
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(cards))
	for _, c := range cards {
		if c != nil {
			out[c.ID] = cardTitleOf(c)
		}
	}
	return out
}

// resolveRequesterName resolves a person-request approval's RequestedBy (a
// roster person id) to that person's display name (internal/roster's
// "people" table). "" on any miss, the same resolution-miss posture
// store.Person.Identity documents for itself.
func (d *Daemon) resolveRequesterName(ctx context.Context, requestedBy string) string {
	if requestedBy == "" {
		return ""
	}
	p, err := store.Get[store.Person](ctx, d.cfg.Store, "seed", requestedBy)
	if err != nil {
		return ""
	}
	return p.Name
}

// enrichApprovalView fills in the ctx/store-dependent fields viewOf can't
// compute on its own: SourceCardTitle (from cardTitles, built once per
// request by sourceCardTitles -- nil is fine, it just resolves nothing) and
// RequestedByName/RequesterInitials (one roster lookup, only when the
// envelope actually names a requester).
func (d *Daemon) enrichApprovalView(ctx context.Context, v ApprovalView, cardTitles map[string]string) ApprovalView {
	if v.SourceCardID != "" && cardTitles != nil {
		v.SourceCardTitle = cardTitles[v.SourceCardID]
	}
	if v.RequestedBy != "" {
		if name := d.resolveRequesterName(ctx, v.RequestedBy); name != "" {
			v.RequestedByName = name
			v.RequesterInitials = Initials(name)
		}
	}
	return v
}

// approvalView is the ctx-aware equivalent of viewOf for exactly one
// envelope: it only pays sourceCardTitles' cost (a decisions run) when this
// envelope actually has a SourceCardID to resolve.
func (d *Daemon) approvalView(ctx context.Context, e approvals.Envelope) ApprovalView {
	var titles map[string]string
	if e.SourceCardID != "" {
		titles = d.sourceCardTitles(ctx)
	}
	return d.enrichApprovalView(ctx, viewOf(e), titles)
}

// approvalViews is approvalView for a batch: sourceCardTitles runs at most
// once for the whole slice, not once per envelope.
func (d *Daemon) approvalViews(ctx context.Context, envs []approvals.Envelope) []ApprovalView {
	var titles map[string]string
	for _, e := range envs {
		if e.SourceCardID != "" {
			titles = d.sourceCardTitles(ctx)
			break
		}
	}
	out := make([]ApprovalView, len(envs))
	for i, e := range envs {
		out[i] = d.enrichApprovalView(ctx, viewOf(e), titles)
	}
	return out
}
