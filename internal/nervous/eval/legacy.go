package eval

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/runtime"
	"water/internal/store"
	"water/internal/twins"
)

// legacyTier wraps the pre-Slice-R internal/runtime.FastPath so it can be
// measured with the same harness real Tier 0/Tier 1 implementations are
// measured with, producing the "Baseline" numbers recorded in
// docs/slices/R.md. FastPath predates the intent registry: it has no
// concept of an intent id or a slot, only three hardcoded matchers
// (schedule, approvals, brief). Try maps a match to the closest of the new
// intent ids by which matcher fired, and never reports slots (old FastPath
// never extracted one) — see the Tier interface's doc comment for why an
// absent slot is not treated as a mismatch.
type legacyTier struct {
	env runtime.Env
}

// newLegacyTier builds a legacyTier against a fresh temp-dir store seeded
// with fx's senders (as received messages, so mail.* intents' underlying
// data exists even though old FastPath never queries it) and a pending
// count controlled per-call via Case.Pending (old FastPath reads
// env.Approvals directly, so pending envelopes are (re)seeded before each
// Try call that declares a nonzero Case.Pending).
func newLegacyTier(t testing.TB, fx Fixture) *legacyTier {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "water.db"))
	if err != nil {
		t.Fatalf("eval: open legacy fixture store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	logPath := filepath.Join(dir, "audit.jsonl")
	auditLog, err := audit.Open(logPath)
	if err != nil {
		t.Fatalf("eval: open legacy fixture audit log: %v", err)
	}
	t.Cleanup(func() { auditLog.Close() })

	q := approvals.NewQueue(st, auditLog)
	q.Now = func() time.Time { return fx.Now }

	m := &twins.Manifest{ID: "ceo-eval-legacy"}

	env := runtime.Env{
		Store:     st,
		Approvals: q,
		Manifest:  m,
		Backend:   backend.NewFake("fake"),
		Now:       func() time.Time { return fx.Now },
	}
	return &legacyTier{env: env}
}

func (lt *legacyTier) Try(ctx context.Context, utterance string, pending int) (matched bool, intent string, slots map[string]string, escalate bool, err error) {
	if pending > 0 {
		for i := 0; i < pending; i++ {
			if _, perr := lt.env.Approvals.Propose(ctx, approvals.Envelope{
				Action:  "fake.action",
				Payload: map[string]any{"n": i},
				Origin:  "P0",
			}); perr != nil {
				return false, "", nil, false, perr
			}
		}
	}

	answer, ok := runtime.FastPath(ctx, lt.env, utterance)

	if pending > 0 {
		// Drain what this call proposed so the next case starts clean;
		// old FastPath's approvalsAnswer only reads, it never decides.
		pendingList, _ := lt.env.Approvals.Pending(ctx)
		for _, e := range pendingList {
			_, _ = lt.env.Approvals.Abandon(ctx, e.ID, "eval fixture cleanup")
		}
	}

	if !ok {
		return false, "", nil, true, nil
	}

	// Map the answer text back to the closest new intent id by which of
	// FastPath's three matchers must have produced it, using the same
	// substring cues FastPath's own matchers look for. This is a coarse,
	// best-effort classification for baseline-measurement purposes only.
	lower := strings.ToLower(answer)
	switch {
	case strings.Contains(lower, "calendar") || strings.Contains(lower, "meeting") || strings.Contains(lower, "nothing on"):
		intent = "schedule.on_date"
	case strings.Contains(lower, "approval"):
		intent = "approvals.list"
	case strings.Contains(lower, "brief"):
		intent = "brief.today"
	default:
		intent = "unknown"
	}
	return true, intent, nil, false, nil
}

var _ Tier = (*legacyTier)(nil)
