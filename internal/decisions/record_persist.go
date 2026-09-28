package decisions

import (
	"context"
	"encoding/json"

	"water/internal/store"
)

// EncodeCardRecord serializes c for storage as a decision_records row's
// card_json (docs/slices/UI.md Phase 1c, U13). Card has no JSON tags of its
// own -- the same convention every other JSON surface it already has (e.g.
// GET /v1/decisions) relies on, plain Go field names -- so this is a plain
// json.Marshal; the function exists only so callers on the store side have
// one documented place to encode/decode a persisted card, matching how the
// rest of this package encodes/decodes across the internal/store boundary.
func EncodeCardRecord(c *Card) (string, error) {
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// DecodeCardRecord decodes a decision_records row's card_json back into a
// Card, the inverse of EncodeCardRecord.
func DecodeCardRecord(cardJSON string) (*Card, error) {
	var c Card
	if err := json.Unmarshal([]byte(cardJSON), &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// MergeFromStore loads every persisted decision_records row and every
// card_evidence_extra row from st and combines them with computed via
// Merge (docs/slices/UI.md Phase 1c, U13): a persisted record wins over a
// computed card sharing its id, and extra evidence is appended to whichever
// version wins, tainted per its own untrusted column. Both GET
// /v1/decisions (internal/gateway/decisions.go) and needsyou.Compute call
// this one path, so a persisted record overrides the live classifier's card
// the same way in both places. A record row whose card_json fails to
// decode is skipped rather than failing the whole call -- one bad seed row
// must not take every other decision down with it. A nil st passes
// computed through unchanged (no store configured, nothing to merge).
func MergeFromStore(ctx context.Context, st *store.Store, computed []*Card) ([]*Card, error) {
	if st == nil {
		return computed, nil
	}
	rows, err := st.ListDecisionRecords(ctx)
	if err != nil {
		return nil, err
	}
	var records []*Card
	for _, row := range rows {
		c, err := DecodeCardRecord(row.CardJSON)
		if err != nil {
			continue
		}
		records = append(records, c)
	}
	extraByCard, err := st.AllCardEvidenceExtra(ctx)
	if err != nil {
		return nil, err
	}
	var extra []EvidenceExtra
	for _, rows := range extraByCard {
		for _, e := range rows {
			extra = append(extra, EvidenceExtra{CardID: e.CardID, Source: e.Source, Text: e.Text, Untrusted: e.Untrusted})
		}
	}
	return Merge(computed, records, extra), nil
}
