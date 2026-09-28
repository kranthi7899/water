package meetings

import (
	"context"
	"strings"
	"time"
)

// CueWindow bounds how recent a segment must be to seed a proactive cue
// (docs/slices/M.md section 6): only what was just said, not the whole
// meeting so far.
const CueWindow = 45 * time.Second

// CueMinInterval is this feature's rate cap: no two non-empty cue batches
// for the same session closer together than this, enforced in code the
// same way gate.Gate's rate windows are (in-memory, per process, reset on
// restart).
const CueMinInterval = 30 * time.Second

// CueLimit bounds how many related items one cue batch returns (2-3 per
// docs/slices/M.md section 6).
const CueLimit = 3

// CueItem is one related item shown in the quiet side panel: enough to
// render and to point back at, nothing more.
type CueItem struct {
	Kind  string `json:"kind"`  // "message" | "document"
	Ref   string `json:"ref"`   // the item's SourceID
	Label string `json:"label"` // subject (message) or title (document)
}

// CueSet is one call's answer. Tainted is always true once the session is
// found, for the same reason HelpContext.Tainted always is: this reads live
// meeting content. Items is nil, not an error, whenever nothing is found or
// the rate limit suppresses this call.
type CueSet struct {
	SessionID string
	Items     []CueItem
	Tainted   bool
}

// cueState is the last non-empty batch Cues returned for one session, kept
// only so the next call can rate-limit against it.
type cueState struct {
	at  time.Time
	key string
}

// Cues computes docs/slices/M.md section 6's quiet proactive cues: 2-3
// locally indexed messages or documents related to whatever was just said
// on either channel, deliberately simple (a keyword match against the FTS5
// index built for on-demand help, reusing Help's own term extraction) —
// the spec calls for glanceable suggestions, not a ranked recommender.
// Never calls a model, never reaches a live connector. Rate-limited two
// ways, per session, in memory: no non-empty batch within CueMinInterval of
// the last one, and never the exact same batch shown twice in a row.
func (m *Manager) Cues(ctx context.Context, sessionID string, now time.Time) (CueSet, error) {
	segs, err := m.SegmentsSince(ctx, sessionID, now.Add(-CueWindow))
	if err != nil {
		return CueSet{}, err
	}
	cs := CueSet{SessionID: sessionID, Tainted: true}
	q := ftsQuery(segs)
	if q == "" {
		return cs, nil
	}
	var items []CueItem
	if msgs, err := m.st.SearchMessages(ctx, q, CueLimit); err == nil {
		for _, h := range msgs {
			items = append(items, CueItem{Kind: "message", Ref: h.SourceID, Label: h.Subject})
		}
	}
	if docs, err := m.st.SearchDocuments(ctx, q, CueLimit); err == nil {
		for _, h := range docs {
			items = append(items, CueItem{Kind: "document", Ref: h.SourceID, Label: h.Title})
		}
	}
	if len(items) == 0 {
		return cs, nil
	}
	if len(items) > CueLimit {
		items = items[:CueLimit]
	}
	key := cueKey(items)
	m.cueMu.Lock()
	prev := m.cueState[sessionID]
	suppress := key == prev.key || now.Sub(prev.at) < CueMinInterval
	if !suppress {
		m.cueState[sessionID] = cueState{at: now, key: key}
	}
	m.cueMu.Unlock()
	if suppress {
		return cs, nil
	}
	cs.Items = items
	return cs, nil
}

func cueKey(items []CueItem) string {
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = it.Kind + ":" + it.Ref
	}
	return strings.Join(parts, "|")
}
