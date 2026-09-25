package nervous

import (
	"sync"
	"time"

	"water/internal/runtime"
)

// Readback is what the CEO heard or saw immediately before a yes/no on a
// voice turn: the exact envelope and payload hash a later "yes" or "no" on
// that same channel may bind a decision to (Design §13). It is recorded only
// where a read-back is actually emitted on a voice turn's stream — a queued
// write-intent proposal (actions.go's tryWriteIntent), approvals.respond
// surfacing the one pending envelope (actions.go's answerVoiceApprove), or a
// model-queued tool call reaching a voice client (the gateway's
// notifyApprovalRequired, via RecordReadback below).
type Readback struct {
	Channel     runtime.Channel
	EnvelopeID  string
	PayloadHash string
	At          time.Time
}

// Readbacks is the single most recent read-back per channel. It lives only
// in memory: a daemon restart loses every binding, which is safe — the CEO
// is simply asked to repeat "yes" against a freshly re-surfaced read-back
// instead of a decision silently applying (or failing to) across a restart.
type Readbacks struct {
	mu   sync.Mutex
	last map[runtime.Channel]Readback
}

// NewReadbacks returns an empty Readbacks.
func NewReadbacks() *Readbacks {
	return &Readbacks{last: map[runtime.Channel]Readback{}}
}

// Record replaces the read-back last shown on rb.Channel.
func (r *Readbacks) Record(rb Readback) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last[rb.Channel] = rb
}

// Void clears whichever channel currently holds envelopeID's read-back, if
// any. A safe no-op when envelopeID was never recorded, was already voided,
// or has since been superseded by a later read-back on its channel.
func (r *Readbacks) Void(envelopeID string) {
	if envelopeID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for ch, rb := range r.last {
		if rb.EnvelopeID == envelopeID {
			delete(r.last, ch)
		}
	}
}

// Bound reports ch's most recently recorded read-back, if one exists and is
// no older than window. An older (or absent) read-back is reported as
// "not bound" — Bound never mutates state itself; a caller that decides a
// read-back is stale is responsible for voiding and re-recording it.
func (r *Readbacks) Bound(ch runtime.Channel, now time.Time, window time.Duration) (Readback, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rb, ok := r.last[ch]
	if !ok || now.Sub(rb.At) > window {
		return Readback{}, false
	}
	return rb, true
}

// RecordReadback lets a caller outside this package (internal/gateway's
// notifyApprovalRequired, for a model-queued tool call reaching a voice
// client) record a read-back without reaching into Nervous's internals.
// Only the voice channel is ever bound against later (Design §13), so a
// non-voice channel is a deliberate no-op rather than dead state.
func (n *Nervous) RecordReadback(ch runtime.Channel, envelopeID, payloadHash string, at time.Time) {
	if ch != runtime.ChannelVoice || envelopeID == "" {
		return
	}
	n.readbacks.Record(Readback{Channel: ch, EnvelopeID: envelopeID, PayloadHash: payloadHash, At: at})
}
