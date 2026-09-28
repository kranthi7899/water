package gateway

import (
	"context"
	"net/http"
	"strings"

	"water/internal/store"
)

// relatedSource is one source GET /v1/decisions/{id}/related lists: either
// one of the card's own SourceItemIDs or one of its Evidence entries'
// Source refs (docs/slices/UI.md Phase 3b, U16). Ref is the raw
// "source:source_id" string exactly as the card carries it (or, for an
// entry that doesn't parse that way, exactly as-is) -- an evidence-line
// source that doesn't resolve to anything still lists as plain text via
// Ref, per the plan's "an evidence-line source stays as plain text". Source
// and ID are that same ref split in two, present only once it parses; they
// are exactly the two fields a WorkspaceMessage {type: "open-external"}
// carries (U16), so the client never has to re-split anything itself.
//
// URL is resolved here, server-side, from the store record's own URL
// field -- never fabricated, never page text, never anything a model
// wrote. Today that is a Linear or GitHub Issue (their own API's "url"
// field, carried through unchanged by internal/connectors/linear and
// .../github into store.Issue.URL), a GitHub commit (store.Commit.URL) or
// a Google Doc/Drive file (store.Document.URL). A ref that resolves to a
// record with no URL of its own (a message, a calendar event) -- or that
// doesn't resolve to any stored record at all -- lists with URL "": the
// client shows it as plain text only, with no external-open affordance.
type relatedSource struct {
	Ref    string `json:"ref"`
	Source string `json:"source,omitempty"`
	ID     string `json:"id,omitempty"`
	Kind   string `json:"kind,omitempty"`  // "issue" | "commit" | "document" | "message" | "event" | "" (unresolved)
	Label  string `json:"label,omitempty"` // a short, code-built display label; "" falls back to Ref
	URL    string `json:"url,omitempty"`
}

// relatedResponse is GET /v1/decisions/{id}/related's body.
type relatedResponse struct {
	CardID  string          `json:"card_id"`
	Sources []relatedSource `json:"sources"`
}

// handleRelatedDecision serves GET /v1/decisions/{id}/related: exactly
// card id's own sources, in the order "View related data (N)"'s own count
// promises (docs/slices/UI.md Phase 3b: N = len(SourceItemIDs) +
// len(Evidence)) -- SourceItemIDs first, then one entry per Evidence line,
// never deduplicated even when two entries name the same underlying ref,
// so the panel's row count always matches the button's own N. A card not
// currently open answers 404, exactly like every other per-card decision
// route.
func (d *Daemon) handleRelatedDecision(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()
	card, err := d.findOpenCard(ctx, id)
	if err != nil {
		cardLookupError(w, id, err)
		return
	}
	refs := make([]string, 0, len(card.SourceItemIDs)+len(card.Evidence))
	refs = append(refs, card.SourceItemIDs...)
	for _, e := range card.Evidence {
		refs = append(refs, e.Source)
	}
	out := make([]relatedSource, 0, len(refs))
	for _, ref := range refs {
		out = append(out, d.resolveRelatedSource(ctx, ref))
	}
	writeJSON(w, http.StatusOK, relatedResponse{CardID: id, Sources: out})
}

// relatedSourceLookups is resolveRelatedSource's fixed, ordered list of
// record types to try -- every store record type that carries its own real
// URL field checked first, so a Linear/GitHub issue or a Drive document
// resolves to it, followed by the two shapes with no URL column at all
// (message, event), which still resolve to a Kind and Label. The order
// only matters for which Kind wins if the same (source, source_id) pair
// were ever upserted as two different record types, which never happens in
// practice (each connector's Source value is unique to its own table).
func (d *Daemon) resolveRelatedSource(ctx context.Context, ref string) relatedSource {
	out := relatedSource{Ref: ref}
	source, sourceID, ok := strings.Cut(ref, ":")
	if !ok || source == "" || sourceID == "" {
		return out
	}
	out.Source, out.ID = source, sourceID
	if issue, err := store.Get[store.Issue](ctx, d.cfg.Store, source, sourceID); err == nil {
		out.Kind, out.Label, out.URL = "issue", issue.Title, issue.URL
		return out
	}
	if commit, err := store.Get[store.Commit](ctx, d.cfg.Store, source, sourceID); err == nil {
		out.Kind, out.Label, out.URL = "commit", commit.Message, commit.URL
		return out
	}
	if doc, err := store.Get[store.Document](ctx, d.cfg.Store, source, sourceID); err == nil {
		out.Kind, out.Label, out.URL = "document", doc.Title, doc.URL
		return out
	}
	if msg, err := store.Get[store.Message](ctx, d.cfg.Store, source, sourceID); err == nil {
		out.Kind, out.Label = "message", msg.Subject
		return out
	}
	if ev, err := store.Get[store.Event](ctx, d.cfg.Store, source, sourceID); err == nil {
		out.Kind, out.Label = "event", ev.Title
		return out
	}
	return out
}
