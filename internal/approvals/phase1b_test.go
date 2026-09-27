package approvals

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"water/internal/audit"
	"water/internal/store"
)

func newPhase1bQueue(t *testing.T) *Queue {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	return NewQueue(st, log)
}

// TestApprovalMetadataRoundTrips: every Phase 1b field a caller sets on
// Propose comes back unchanged from Get, and OriginKind/Kind default
// sensibly when the caller leaves them unset.
func TestApprovalMetadataRoundTrips(t *testing.T) {
	q := newPhase1bQueue(t)
	ctx := context.Background()
	deadline := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)

	full, err := q.Propose(ctx, Envelope{
		Action: "gmail.send_message", Origin: "p0",
		Payload:      map[string]any{"to": []any{"a@x.com"}, "subject": "s", "body": "b"},
		OriginKind:   OriginKindAgentDraft,
		RequestedBy:  "lee",
		Kind:         KindEmail,
		SourceCardID: "card_meridian",
		Priority:     "urgent",
		Deadline:     deadline,
		Provenance:   "demo_seed",
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := q.Get(ctx, full.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.OriginKind != OriginKindAgentDraft || got.RequestedBy != "lee" || got.Kind != KindEmail ||
		got.SourceCardID != "card_meridian" || got.Priority != "urgent" || got.Provenance != "demo_seed" {
		t.Fatalf("metadata did not round-trip: %+v", got)
	}
	if !got.Deadline.Equal(deadline) {
		t.Fatalf("deadline = %v, want %v", got.Deadline, deadline)
	}
	// thread_ref/sent_at/replied_at/reply_ref default empty/zero until the
	// trail's later stages set them (see MarkApprovalSent/MarkApprovalReplied
	// tests in package store).
	if got.ThreadRef != "" || !got.SentAt.IsZero() || !got.RepliedAt.IsZero() || got.ReplyRef != "" {
		t.Fatalf("trail fields not empty on a fresh envelope: %+v", got)
	}

	// A caller that sets nothing gets the documented defaults.
	bare, err := q.Propose(ctx, Envelope{Action: "notes.save_note", Origin: "p0", Payload: map[string]any{"text": "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	got2, err := q.Get(ctx, bare.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got2.OriginKind != OriginKindAgentDraft {
		t.Fatalf("default origin_kind = %q, want %q", got2.OriginKind, OriginKindAgentDraft)
	}
	if got2.Kind != "" {
		t.Fatalf("default kind = %q, want empty for an unmapped action", got2.Kind)
	}
	if !got2.Deadline.IsZero() {
		t.Fatalf("default deadline = %v, want zero", got2.Deadline)
	}
}

// TestPayloadHash_UnchangedByPhase1bMetadata is the invariant the whole
// migration depends on: an approval's payload-hash binding is only ever the
// thing the CEO is actually approving. Two envelopes whose Payload is
// byte-for-byte identical but whose Phase 1b metadata differs in every field
// must hash identically.
func TestPayloadHash_UnchangedByPhase1bMetadata(t *testing.T) {
	payload := map[string]any{"to": []any{"a@x.com"}, "subject": "s", "body": "b"}
	bare, err := PayloadHash(payload)
	if err != nil {
		t.Fatal(err)
	}
	dressed := Envelope{
		Action: "gmail.send_message", Payload: payload,
		OriginKind: OriginKindPersonRequest, RequestedBy: "lee", Kind: KindMoney,
		SourceCardID: "card_x", Priority: "urgent", Deadline: time.Now(),
		ThreadRef: "thread_1", SentAt: time.Now(), RepliedAt: time.Now(), ReplyRef: "msg_1",
		Provenance: "demo_seed",
	}
	dressedHash, err := PayloadHash(dressed.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if dressedHash != bare {
		t.Fatalf("PayloadHash changed with metadata set: %q vs %q", dressedHash, bare)
	}

	// End to end through Propose/Get too, not just the pure function: two
	// envelopes with the same payload but opposite metadata must Propose to
	// the same payload_hash.
	q := newPhase1bQueue(t)
	ctx := context.Background()
	e1, err := q.Propose(ctx, Envelope{Action: "notes.save_note", Origin: "p0", Payload: map[string]any{"text": "same"}})
	if err != nil {
		t.Fatal(err)
	}
	e2, err := q.Propose(ctx, Envelope{
		Action: "notes.save_note", Origin: "p1", Payload: map[string]any{"text": "same"},
		OriginKind: OriginKindPersonRequest, RequestedBy: "riley", Kind: KindSignature,
		SourceCardID: "card_y", Priority: "high", Provenance: "demo_seed",
	})
	if err != nil {
		t.Fatal(err)
	}
	if e1.PayloadHash != e2.PayloadHash {
		t.Fatalf("payload_hash differs despite identical payload: %q vs %q", e1.PayloadHash, e2.PayloadHash)
	}
}

// TestDeriveKind: Propose's Kind lookup is correct for every mapped action,
// derives requests.respond's fallback only when the caller didn't already
// set Kind, and falls back to "" for anything else unmapped.
func TestDeriveKind(t *testing.T) {
	q := newPhase1bQueue(t)
	ctx := context.Background()
	cases := []struct {
		name string
		e    Envelope
		want string
	}{
		{"gmail.send_message", Envelope{Action: "gmail.send_message", Origin: "p0", Payload: map[string]any{"to": []any{"a@x.com"}, "subject": "s", "body": "b"}}, KindEmail},
		{"gmail.draft_message", Envelope{Action: "gmail.draft_message", Origin: "p0", Payload: map[string]any{"to": []any{"a@x.com"}, "subject": "s", "body": "b"}}, KindEmail},
		{"requests.respond with no caller Kind", Envelope{Action: "requests.respond", RequestedBy: "lee", Origin: "p1", Payload: map[string]any{"request_id": "r1", "answer": "ok"}}, KindMessage},
		{"an unmapped action", Envelope{Action: "notes.save_note", Origin: "p0", Payload: map[string]any{"text": "hi"}}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := q.Propose(ctx, c.e)
			if err != nil {
				t.Fatal(err)
			}
			if got.Kind != c.want {
				t.Fatalf("kind = %q, want %q", got.Kind, c.want)
			}
		})
	}
	// A caller that already set Kind wins over both functionKind and the
	// requests.respond fallback.
	explicit, err := q.Propose(ctx, Envelope{Action: "gmail.send_message", Origin: "p0", Kind: KindFlag,
		Payload: map[string]any{"to": []any{"a@x.com"}, "subject": "s", "body": "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Kind != KindFlag {
		t.Fatalf("caller's explicit kind was overridden: %q", explicit.Kind)
	}
	viaRequest, err := q.ProposeRequest(ctx, Envelope{RequestedBy: "lee", Origin: "p1", Kind: KindMoney,
		Payload: map[string]any{"request_id": "r2", "answer": "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	if viaRequest.Kind != KindMoney {
		t.Fatalf("ProposeRequest caller's explicit kind was overridden: %q", viaRequest.Kind)
	}
}

// TestTrail: the derived status trail reads staged -> approved -> sent ->
// reply purely off Status plus SentAt/RepliedAt, never off Status alone for
// the last two stages.
func TestTrail(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		e    Envelope
		want TrailStage
	}{
		{"pending is staged", Envelope{Status: Pending}, TrailStaged},
		{"approved is approved", Envelope{Status: Approved}, TrailApproved},
		{"denied has no trail stage", Envelope{Status: Denied}, ""},
		{"expired has no trail stage", Envelope{Status: Expired}, ""},
		{"executed with no sent_at proves nothing", Envelope{Status: Executed}, ""},
		{"executed with sent_at is sent", Envelope{Status: Executed, SentAt: now}, TrailSent},
		{"executed with sent_at and replied_at is reply", Envelope{Status: Executed, SentAt: now, RepliedAt: now.Add(time.Hour)}, TrailReply},
		{"executed with only replied_at (no sent_at) is not reply", Envelope{Status: Executed, RepliedAt: now}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.e.Trail(); got != c.want {
				t.Fatalf("Trail() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestRequestTTL: a demo-seeded envelope of any origin_kind, and any
// person-request envelope (demo or not), get RequestTTL; an ordinary agent
// draft keeps DefaultTTL.
func TestRequestTTL(t *testing.T) {
	q := newPhase1bQueue(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	q.Now = func() time.Time { return now }

	agentDraft, err := q.Propose(ctx, Envelope{Action: "notes.save_note", Origin: "p0", Payload: map[string]any{"text": "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(DefaultTTL); !agentDraft.ExpiresAt.Equal(want) {
		t.Fatalf("agent draft expires_at = %v, want %v", agentDraft.ExpiresAt, want)
	}

	demoSeeded, err := q.Propose(ctx, Envelope{Action: "notes.save_note", Origin: "p0", Payload: map[string]any{"text": "hi2"}, Provenance: "demo_seed"})
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(RequestTTL); !demoSeeded.ExpiresAt.Equal(want) {
		t.Fatalf("demo_seed expires_at = %v, want %v", demoSeeded.ExpiresAt, want)
	}

	personRequest, err := q.ProposeRequest(ctx, Envelope{RequestedBy: "lee", Origin: "p1", Payload: map[string]any{"request_id": "r1", "answer": "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(RequestTTL); !personRequest.ExpiresAt.Equal(want) {
		t.Fatalf("person_request (non-demo) expires_at = %v, want %v", personRequest.ExpiresAt, want)
	}

	both, err := q.ProposeRequest(ctx, Envelope{RequestedBy: "lee", Origin: "p1", Provenance: "demo_seed", Payload: map[string]any{"request_id": "r2", "answer": "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(RequestTTL); !both.ExpiresAt.Equal(want) {
		t.Fatalf("demo_seed person_request expires_at = %v, want %v", both.ExpiresAt, want)
	}

	// An explicit ExpiresAt (e.g. agentmail's staged forward) is never
	// overridden by ttlFor.
	explicit, err := q.Propose(ctx, Envelope{Action: "notes.save_note", Origin: "p0", Payload: map[string]any{"text": "hi3"}, ExpiresAt: now.Add(72 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if want := now.Add(72 * time.Hour); !explicit.ExpiresAt.Equal(want) {
		t.Fatalf("explicit expires_at was overridden: %v, want %v", explicit.ExpiresAt, want)
	}
}
