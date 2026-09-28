package decisions

// EvidenceExtra is evidence attached to an existing card after the fact
// (docs/slices/UI.md Phase 1c; the store-layer shape is
// store.CardEvidenceExtra -- a caller converts between the two, the same
// layering build.go already keeps between this package and internal/store).
// It always carries its own taint: Merge never drops Untrusted silently.
type EvidenceExtra struct {
	CardID    string
	Source    string
	Text      string
	Untrusted bool
}

// Merge combines the cards the live classifier/trigger just computed with
// whatever decision_records rows are persisted (U13), plus any
// card_evidence_extra rows, into the single list GET /v1/decisions and
// needsyou both serve.
//
// Semantics:
//   - A persisted record wins over a computed card sharing its ID: once a
//     card id has a decision_records row, that row's fields show through in
//     full (this is the whole point of U13 -- a card can be pinned,
//     hand-edited or demo-seeded). Nothing from the computed version
//     survives for that id.
//   - Every extra evidence row for an id is appended to whichever version
//     won (computed or record-sourced), in order, regardless of source.
//   - Appended evidence carries its own taint (Evidence.Untrusted, set from
//     the row's own Untrusted), and an untrusted append also sets the
//     merged card's own Untrusted -- a card that was otherwise fully
//     trusted must read as untrusted once tainted evidence is merged in;
//     Merge never drops that silently.
//   - An id present only in records (no matching computed card) still
//     appears in the result. A card with neither a record nor extra
//     evidence passes through unchanged, and is not copied (same pointer as
//     the input) so a caller that already has no reason to treat it
//     specially doesn't pay for one.
//   - Order: every computed-only id first (Rank still runs on the result
//     downstream, exactly as it does today, so this ordering is not the
//     final one), then any record-only ids, each in their own input order.
func Merge(computed []*Card, records []*Card, extra []EvidenceExtra) []*Card {
	byID := map[string]*Card{}
	var order []string
	for _, c := range computed {
		if c == nil || c.ID == "" {
			continue
		}
		byID[c.ID] = c
		order = append(order, c.ID)
	}
	for _, r := range records {
		if r == nil || r.ID == "" {
			continue
		}
		if _, ok := byID[r.ID]; !ok {
			order = append(order, r.ID)
		}
		byID[r.ID] = r // a persisted record always wins over a computed card
	}

	extraByID := map[string][]EvidenceExtra{}
	for _, e := range extra {
		if e.CardID == "" {
			continue
		}
		extraByID[e.CardID] = append(extraByID[e.CardID], e)
	}

	out := make([]*Card, 0, len(order))
	for _, id := range order {
		c := byID[id]
		items := extraByID[id]
		if len(items) == 0 {
			out = append(out, c)
			continue
		}
		merged := *c // shallow copy: appending evidence must not mutate the caller's slice
		merged.Evidence = append(append([]Evidence(nil), c.Evidence...), evidenceFromExtra(items)...)
		for _, e := range items {
			if e.Untrusted {
				merged.Untrusted = true
			}
		}
		out = append(out, &merged)
	}
	return out
}

func evidenceFromExtra(items []EvidenceExtra) []Evidence {
	out := make([]Evidence, 0, len(items))
	for _, e := range items {
		out = append(out, Evidence{Text: e.Text, Source: e.Source, Untrusted: e.Untrusted})
	}
	return out
}
