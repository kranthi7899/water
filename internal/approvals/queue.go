// Package approvals holds draft envelopes and the persisted approval queue.
// An approval is bound to the sha256 of the canonical payload: any edit
// voids it, it can be used once, and expiry, silence or ambiguity are "no".
package approvals

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"water/internal/audit"
	"water/internal/canon"
	"water/internal/store"
)

type Status string

const (
	Pending  Status = "pending"
	Approved Status = "approved"
	Denied   Status = "denied"
	Expired  Status = "expired"
	Executed Status = "executed"
)

// Envelope is an outward action awaiting, or bound by, the CEO's decision.
// Its JSON form (snake_case, matching the rest of the daemon API) is what
// GET /v1/approvals and the decision endpoint return; it is not how the
// envelope is persisted (the store keeps it as columns).
type Envelope struct {
	ID           string         `json:"id"`
	Action       string         `json:"action"` // "connector.function"
	Recipient    string         `json:"recipient"`
	Payload      map[string]any `json:"payload"`
	EvidenceRefs []string       `json:"evidence_refs"`
	Risk         string         `json:"risk"`
	Origin       string         `json:"origin"`
	ExpiresAt    time.Time      `json:"expires_at"`

	PayloadHash string    `json:"payload_hash"`
	Status      Status    `json:"status"`
	Reason      string    `json:"reason"`
	CreatedAt   time.Time `json:"created_at"`

	// Warnings are code-built recipient warnings (an unusual domain the CEO
	// confirmed, a mail domain with no mail server or that could not be
	// verified). They are not persisted and not covered by PayloadHash:
	// the Queue recomputes them on every read (see RecipientChecker), so
	// they are always current and never something a caller can set.
	Warnings []string `json:"warnings,omitempty"`

	// OriginKind through Provenance are Phase 1b's approval metadata and
	// derived-trail fields (docs/slices/UI.md). None of them are part of
	// PayloadHash: PayloadHash only ever hashes Payload (see PayloadHash
	// below), so editing any of these can never change, or be mistaken for
	// changing, the thing the CEO is actually approving. See
	// TestPayloadHash_UnchangedByPhase1bMetadata.
	OriginKind   string    `json:"origin_kind"`  // "agent_draft"|"person_request" (U21)
	RequestedBy  string    `json:"requested_by"` // a roster person id, for a person request
	Kind         string    `json:"kind"`         // "email"|"money"|"signature"|"flag"|"message", for icon choice
	SourceCardID string    `json:"source_card_id"`
	Priority     string    `json:"priority"`
	Deadline     time.Time `json:"deadline"`
	ThreadRef    string    `json:"thread_ref"` // the Gmail threadId a send executed into
	SentAt       time.Time `json:"sent_at"`    // when the action executed (the trail's "sent" stage)
	RepliedAt    time.Time `json:"replied_at"` // when an inbound message on ThreadRef arrived
	ReplyRef     string    `json:"reply_ref"`  // that inbound message's own id
	Provenance   string    `json:"provenance"` // "" or "demo_seed" (U21)
}

// Origin-kind values for Envelope.OriginKind (U21). The zero value defaults
// to OriginKindAgentDraft at Propose, so every pre-1b envelope (proposed
// only by the model, never by a person) reads back correctly.
const (
	OriginKindAgentDraft    = "agent_draft"
	OriginKindPersonRequest = "person_request"
)

// Kind values for Envelope.Kind, used only for icon choice (docs/slices/UI.md
// Phase 1b).
const (
	KindEmail     = "email"
	KindMoney     = "money"
	KindSignature = "signature"
	KindFlag      = "flag"
	KindMessage   = "message"
)

// TrailStage is where the derived approval status trail (docs/slices/UI.md
// Phase 1b) currently reads: staged -> approved -> sent -> reply. It is
// never a stored column -- Envelope.Trail computes it fresh from Status plus
// SentAt/RepliedAt every time it's asked, the same way Warnings is computed
// fresh from the recipient checks rather than stored.
type TrailStage string

const (
	TrailStaged   TrailStage = "staged"
	TrailApproved TrailStage = "approved"
	TrailSent     TrailStage = "sent"
	TrailReply    TrailStage = "reply"
)

// Trail computes e's position on the derived status trail. Denied and
// Expired are terminal, non-trail outcomes and Trail returns "" for them; a
// caller showing the trail falls back to Status directly in that case.
// "sent" and "reply" are only ever reported when the timestamp that stage
// actually means is set -- never guessed from Status alone -- so an Executed
// envelope whose SentAt was never recorded (there should be none once
// decideAndExecute always calls store.MarkApprovalSent, but an envelope
// executed before this migration landed has no sent_at) also reads back as
// "", not as a stage it cannot actually prove.
func (e Envelope) Trail() TrailStage {
	switch e.Status {
	case Pending:
		return TrailStaged
	case Approved:
		return TrailApproved
	case Executed:
		if e.SentAt.IsZero() {
			return ""
		}
		if !e.RepliedAt.IsZero() {
			return TrailReply
		}
		return TrailSent
	default:
		return ""
	}
}

var (
	ErrNotFound    = errors.New("approval not found")
	ErrNotApproved = errors.New("envelope is not approved")
	ErrExpired     = errors.New("approval expired")
	ErrMismatch    = errors.New("payload does not match the approved envelope")
	ErrTampered    = errors.New("stored envelope does not match its hash")
)

// DefaultTTL bounds how long an unanswered or unused approval lives.
const DefaultTTL = 15 * time.Minute

// RequestTTL is the longer TTL ttlFor gives a demo-seeded or person-request
// envelope instead of DefaultTTL's 15 minutes (docs/slices/UI.md Phase 1b,
// finding 21): a demo-seeded envelope of any kind must survive an entire
// scripted walkthrough rather than expiring mid-demo, and a real person
// request (U15) may reasonably sit waiting on the CEO's attention for days,
// not minutes.
const RequestTTL = 7 * 24 * time.Hour

// ttlFor is the TTL Propose gives e when it doesn't already set ExpiresAt
// itself: RequestTTL for a demo-seeded envelope of any origin_kind, or for
// any person-request envelope (demo or not); DefaultTTL otherwise. This is
// an OR, by design: finding 21's demo-survival need and U15's person-request
// need are two independent reasons for a longer TTL, not one combined
// condition -- a demo-seeded agent draft (finding 21 alone) and a live,
// non-demo person request (U15 alone) both need it.
func ttlFor(e Envelope) time.Duration {
	if e.Provenance == "demo_seed" || e.OriginKind == OriginKindPersonRequest {
		return RequestTTL
	}
	return DefaultTTL
}

// functionKind maps an outward action to the Kind (docs/slices/UI.md Phase
// 1b: email|money|signature|flag|message, for icon choice) a proposed
// envelope gets when the caller doesn't set Kind itself. Consulted by
// deriveKind only when e.Kind == "".
var functionKind = map[string]string{
	"gmail.send_message":  KindEmail,
	"gmail.draft_message": KindEmail,
	// linear.set_issue_priority would map to KindFlag (docs/slices/UI.md
	// U4), but internal/connectors/linear has no set_issue_priority
	// function yet -- add the entry once U4 adds it.
}

// deriveKind is Propose's Kind lookup, run only when the caller left Kind
// "". functionKind covers every action with one fixed kind. requests.respond
// (U15's person-request connector) has no fixed kind: what's being asked for
// varies per request (money, a signature, ...), and this package has no
// structured view of that beyond the free-text payload ProposeRequest's
// caller already built. So a ProposeRequest caller that knows better should
// set Envelope.Kind itself (e.g. "money" for a budget-reallocation ask)
// before calling; deriveKind's "message" is only the safe fallback per
// docs/slices/UI.md Phase 1b when it doesn't. Anything else unmapped
// defaults to "", the same as the kind column's own default.
func deriveKind(action string) string {
	if k, ok := functionKind[action]; ok {
		return k
	}
	if action == "requests.respond" {
		return KindMessage
	}
	return ""
}

type Queue struct {
	st  *store.Store
	log *audit.Log
	Now func() time.Time

	// beforeTransition, when set (tests only), runs in Decide between the
	// pending check and the compare-and-swap, to interleave a racing decider.
	beforeTransition func()

	// recipients runs the recipient checks for gmail writes (see
	// SetRecipientChecker). Nil runs the pure checks only, with no DNS.
	recipients *RecipientChecker
}

// SetRecipientChecker attaches the checker Propose and every read use for
// gmail recipients: nil (the default, and what tests get) runs only the
// pure checks (syntax, near-miss against the public providers) and never
// touches DNS.
func (q *Queue) SetRecipientChecker(c *RecipientChecker) { q.recipients = c }

// withWarnings sets e.Warnings from the current recipient checks. It is
// applied to every envelope the Queue returns, so warnings are never
// stored, never hashed, and always current.
// Only an envelope still awaiting a decision or its execution carries
// them: a decided one's warnings no longer change anything.
func (q *Queue) withWarnings(e Envelope) Envelope {
	if e.Status == Pending || e.Status == Approved {
		e.Warnings = q.recipients.Warnings(e)
	}
	return e
}

func NewQueue(st *store.Store, log *audit.Log) *Queue {
	return &Queue{st: st, log: log, Now: time.Now}
}

// PayloadHash is the sha256 of the canonical (sorted-key) payload.
func PayloadHash(payload map[string]any) (string, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	return canon.Hash(payload)
}

func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "env_" + hex.EncodeToString(b[:])
}

// Propose adds a pending envelope. A gmail write whose recipient fails the
// recipient checks (an invalid address, or a domain that looks misheard and
// was not confirmed) is refused with an error wrapping ErrRecipient, and
// nothing is proposed or audited.
func (q *Queue) Propose(ctx context.Context, e Envelope) (Envelope, error) {
	if e.Action == "" || e.Origin == "" {
		return Envelope{}, errors.New("approvals: action and origin are required")
	}
	if _, err := q.recipients.Check(ctx, e.Action, e.Payload); err != nil {
		return Envelope{}, err
	}
	if e.OriginKind == "" {
		e.OriginKind = OriginKindAgentDraft
	}
	if e.Kind == "" {
		e.Kind = deriveKind(e.Action)
	}
	now := q.Now().UTC()
	if e.ExpiresAt.IsZero() {
		e.ExpiresAt = now.Add(ttlFor(e))
	}
	if !e.ExpiresAt.After(now) {
		return Envelope{}, ErrExpired
	}
	if e.Payload == nil {
		e.Payload = map[string]any{}
	}
	body, err := canon.JSON(e.Payload)
	if err != nil {
		return Envelope{}, err
	}
	e.PayloadHash, err = PayloadHash(e.Payload)
	if err != nil {
		return Envelope{}, err
	}
	e.ID, e.Status, e.Reason, e.CreatedAt = newID(), Pending, "", now
	if _, err := q.log.Append(audit.Record{Kind: audit.KindPropose, Function: e.Action, EnvelopeID: e.ID, Origin: e.Origin, Allowed: true, Reason: "awaiting approval", ArgsHash: e.PayloadHash}); err != nil {
		return Envelope{}, err
	}
	row := store.ApprovalRow{ID: e.ID, Action: e.Action, Recipient: e.Recipient, Payload: string(body), PayloadHash: e.PayloadHash,
		EvidenceRefs: e.EvidenceRefs, Risk: e.Risk, Origin: e.Origin, Status: string(Pending), CreatedAt: now, ExpiresAt: e.ExpiresAt.UTC(),
		OriginKind: e.OriginKind, RequestedBy: e.RequestedBy, Kind: e.Kind, SourceCardID: e.SourceCardID, Priority: e.Priority,
		Deadline: e.Deadline, Provenance: e.Provenance}
	if err := q.st.InsertApproval(ctx, row); err != nil {
		return Envelope{}, err
	}
	return q.Get(ctx, e.ID)
}

// ProposeRequest proposes a person-request approval (U15): the CEO is being
// asked to answer someone else's request (e.g. Lee's budget-reallocation
// ask), not asked to approve an outward action. It forces
// Action="requests.respond" and OriginKind=OriginKindPersonRequest, and
// requires RequestedBy (the requester's roster person id) -- everything else
// (Payload, Origin, Risk, Kind, SourceCardID, Priority, Deadline, Provenance)
// is the caller's, exactly like Propose. Approving the result executes
// requests.respond through Gate.Invoke like any other envelope: it records
// the answer already sitting in Payload and sends nothing (see
// internal/connectors/requests). The gate stays the only execution path;
// there is no separate code path for a person request's approval.
func (q *Queue) ProposeRequest(ctx context.Context, e Envelope) (Envelope, error) {
	if e.RequestedBy == "" {
		return Envelope{}, errors.New("approvals: ProposeRequest requires RequestedBy")
	}
	e.Action = "requests.respond"
	e.OriginKind = OriginKindPersonRequest
	return q.Propose(ctx, e)
}

// Get loads an envelope and checks that its stored payload still hashes to
// the stored hash.
func (q *Queue) Get(ctx context.Context, id string) (Envelope, error) {
	row, err := q.st.GetApproval(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		return Envelope{}, ErrNotFound
	}
	if err != nil {
		return Envelope{}, err
	}
	e, err := fromRow(row)
	if err != nil {
		return Envelope{}, err
	}
	return q.withWarnings(e), nil
}

func fromRow(r store.ApprovalRow) (Envelope, error) {
	e := Envelope{ID: r.ID, Action: r.Action, Recipient: r.Recipient, EvidenceRefs: r.EvidenceRefs, Risk: r.Risk, Origin: r.Origin,
		ExpiresAt: r.ExpiresAt, PayloadHash: r.PayloadHash, Status: Status(r.Status), Reason: r.Reason, CreatedAt: r.CreatedAt,
		OriginKind: r.OriginKind, RequestedBy: r.RequestedBy, Kind: r.Kind, SourceCardID: r.SourceCardID, Priority: r.Priority,
		Deadline: r.Deadline, ThreadRef: r.ThreadRef, SentAt: r.SentAt, RepliedAt: r.RepliedAt, ReplyRef: r.ReplyRef, Provenance: r.Provenance}
	if err := decodeJSON(r.Payload, &e.Payload); err != nil {
		return Envelope{}, fmt.Errorf("%w: %v", ErrTampered, err)
	}
	if h, err := PayloadHash(e.Payload); err != nil || h != r.PayloadHash {
		return Envelope{}, fmt.Errorf("%w: %s", ErrTampered, r.ID)
	}
	return e, nil
}

// Pending returns undecided envelopes, oldest first, after expiring stale ones.
func (q *Queue) Pending(ctx context.Context) ([]Envelope, error) {
	if err := q.ExpireStale(ctx); err != nil {
		return nil, err
	}
	rows, err := q.st.ListApprovals(ctx, string(Pending))
	if err != nil {
		return nil, err
	}
	out := make([]Envelope, 0, len(rows))
	for _, r := range rows {
		e, err := fromRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, q.withWarnings(e))
	}
	return out, nil
}

// ExpireStale moves pending and approved envelopes past their expiry to
// expired: silence is a "no".
func (q *Queue) ExpireStale(ctx context.Context) error {
	now := q.Now().UTC()
	for _, st := range []Status{Pending, Approved} {
		rows, err := q.st.ListApprovals(ctx, string(st))
		if err != nil {
			return err
		}
		for _, r := range rows {
			if now.Before(r.ExpiresAt) {
				continue
			}
			if err := q.expire(ctx, r.ID, r.Action, st); err != nil {
				return err
			}
		}
	}
	return nil
}

func (q *Queue) expire(ctx context.Context, id, action string, from Status) error {
	ok, err := q.st.TransitionApproval(ctx, id, string(from), string(Expired), "expired", q.Now().UTC())
	if err != nil || !ok {
		return err
	}
	_, err = q.log.Append(audit.Record{Kind: audit.KindDenial, Function: action, EnvelopeID: id, Reason: "expired: treated as no"})
	return err
}

// Decide records the CEO's answer. Only Yes approves; No and Ambiguous
// deny. An envelope past its expiry is expired whatever the answer.
func (q *Queue) Decide(ctx context.Context, id string, a Answer) (Envelope, error) {
	e, err := q.Get(ctx, id)
	if err != nil {
		return Envelope{}, err
	}
	if e.Status != Pending {
		return e, fmt.Errorf("approvals: %s is %s, not pending", id, e.Status)
	}
	now := q.Now().UTC()
	if !now.Before(e.ExpiresAt) {
		if err := q.expire(ctx, id, e.Action, Pending); err != nil {
			return Envelope{}, err
		}
		return q.getAfter(ctx, id, ErrExpired)
	}
	if h := q.beforeTransition; h != nil {
		h()
	}
	if a != Yes {
		reason := "answered no"
		if a != No {
			reason = "ambiguous answer: treated as no"
		}
		ok, err := q.st.TransitionApproval(ctx, id, string(Pending), string(Denied), reason, now)
		if err != nil {
			return Envelope{}, err
		}
		if !ok {
			// Another decider (or expiry) won the compare-and-swap: this
			// answer was not applied, so it is not recorded as a denial.
			// The current envelope comes back with the error so the caller
			// can show what won; callers never act on an errored Decide.
			return q.getAfter(ctx, id, fmt.Errorf("approvals: %s changed while deciding; this answer was not applied", id))
		}
		if _, err := q.log.Append(audit.Record{Kind: audit.KindDenial, Function: e.Action, EnvelopeID: id, Origin: e.Origin, Reason: reason, ArgsHash: e.PayloadHash}); err != nil {
			return Envelope{}, err
		}
		return q.Get(ctx, id)
	}
	ok, err := q.st.TransitionApproval(ctx, id, string(Pending), string(Approved), "answered yes", now)
	if err != nil {
		return Envelope{}, err
	}
	if !ok {
		// As on the deny path: this answer was not applied, and the current
		// envelope comes back with the error so the caller can show what won.
		return q.getAfter(ctx, id, fmt.Errorf("approvals: %s changed while deciding; this answer was not applied", id))
	}
	if _, err := q.log.Append(audit.Record{Kind: audit.KindApproval, Function: e.Action, EnvelopeID: id, Origin: e.Origin, Allowed: true, Reason: "answered yes", ArgsHash: e.PayloadHash}); err != nil {
		// An approval that is not on the record must not stand.
		_, _ = q.st.TransitionApproval(ctx, id, string(Approved), string(Denied), "audit write failed", now)
		return Envelope{}, err
	}
	return q.Get(ctx, id)
}

// Reject denies a pending envelope with a caller-supplied reason: the same
// pending -> denied transition, expiry check and audit trail as Decide's own
// no/ambiguous path, factored out so a caller with a fixed reason that isn't
// "the CEO answered no" (docs/slices/UI.md Phase 3a's request-changes
// endpoint uses "changes requested") doesn't have to fake an Answer to get
// there. Same preconditions as Decide: id must be Pending -- an envelope
// already past its expiry is expired instead, exactly like Decide -- and a
// lost compare-and-swap race reports the current envelope with an error
// rather than a denial that didn't actually apply.
func (q *Queue) Reject(ctx context.Context, id, reason string) (Envelope, error) {
	e, err := q.Get(ctx, id)
	if err != nil {
		return Envelope{}, err
	}
	if e.Status != Pending {
		return e, fmt.Errorf("approvals: %s is %s, not pending", id, e.Status)
	}
	now := q.Now().UTC()
	if !now.Before(e.ExpiresAt) {
		if err := q.expire(ctx, id, e.Action, Pending); err != nil {
			return Envelope{}, err
		}
		return q.getAfter(ctx, id, ErrExpired)
	}
	if h := q.beforeTransition; h != nil {
		h()
	}
	ok, err := q.st.TransitionApproval(ctx, id, string(Pending), string(Denied), reason, now)
	if err != nil {
		return Envelope{}, err
	}
	if !ok {
		return q.getAfter(ctx, id, fmt.Errorf("approvals: %s changed while deciding; this answer was not applied", id))
	}
	if _, err := q.log.Append(audit.Record{Kind: audit.KindDenial, Function: e.Action, EnvelopeID: id, Origin: e.Origin, Reason: reason, ArgsHash: e.PayloadHash}); err != nil {
		return Envelope{}, err
	}
	return q.Get(ctx, id)
}

// Edit voids a pending or approved envelope and proposes the edited payload
// as a new pending envelope that needs its own decision.
func (q *Queue) Edit(ctx context.Context, id string, payload map[string]any) (Envelope, error) {
	old, err := q.Get(ctx, id)
	if err != nil {
		return Envelope{}, err
	}
	if old.Status != Pending && old.Status != Approved {
		return Envelope{}, fmt.Errorf("approvals: %s is %s and cannot be edited", id, old.Status)
	}
	// An edit whose new recipients would be refused leaves the old
	// envelope standing: check before voiding it (Propose checks again).
	if _, err := q.recipients.Check(ctx, old.Action, payload); err != nil {
		return Envelope{}, err
	}
	ok, err := q.st.TransitionApproval(ctx, id, string(old.Status), string(Denied), "voided by edit", q.Now().UTC())
	if err != nil {
		return Envelope{}, err
	}
	if !ok {
		return Envelope{}, fmt.Errorf("approvals: %s changed while editing", id)
	}
	newHash, err := PayloadHash(payload)
	if err != nil {
		return Envelope{}, err
	}
	if _, err := q.log.Append(audit.Record{Kind: audit.KindEdit, Function: old.Action, EnvelopeID: id, Origin: old.Origin, Reason: "voided by edit; new payload " + newHash, ArgsHash: old.PayloadHash}); err != nil {
		return Envelope{}, err
	}
	next := old
	next.Payload = payload
	next.ExpiresAt = time.Time{}
	return q.Propose(ctx, next)
}

// Claim consumes an approved envelope for one execution of action with a
// payload hashing to payloadHash. A different payload is an edit and voids
// the approval; a second claim finds it already executed.
func (q *Queue) Claim(ctx context.Context, id, action, payloadHash string) (Envelope, error) {
	e, err := q.Get(ctx, id)
	if err != nil {
		return Envelope{}, err
	}
	if e.Status != Approved {
		return e, fmt.Errorf("%w: %s is %s", ErrNotApproved, id, e.Status)
	}
	now := q.Now().UTC()
	if !now.Before(e.ExpiresAt) {
		if err := q.expire(ctx, id, e.Action, Approved); err != nil {
			return Envelope{}, err
		}
		return e, ErrExpired
	}
	if e.Action != action || e.PayloadHash != payloadHash {
		reason := "voided: payload changed after approval"
		if e.Action != action {
			reason = "voided: approved for " + e.Action + ", called as " + action
		}
		if _, err := q.st.TransitionApproval(ctx, id, string(Approved), string(Denied), reason, now); err != nil {
			return Envelope{}, err
		}
		if _, err := q.log.Append(audit.Record{Kind: audit.KindEdit, Function: action, EnvelopeID: id, Origin: e.Origin, Reason: reason, ArgsHash: payloadHash}); err != nil {
			return Envelope{}, err
		}
		return e, ErrMismatch
	}
	ok, err := q.st.TransitionApproval(ctx, id, string(Approved), string(Executed), "executed", now)
	if err != nil {
		return Envelope{}, err
	}
	if !ok {
		return e, fmt.Errorf("%w: %s was already used", ErrNotApproved, id)
	}
	e.Status = Executed
	return e, nil
}

// RevertClaim undoes Claim's transition to Executed for a caller that could
// not go on to record that the claim happened (the gate's own audit write
// right after a successful Claim failing is the one case this exists for).
// Without it, that window leaves the envelope stuck Executed forever even
// though the connector never ran: it cannot be reclaimed (Claim only accepts
// Approved), and nothing ever surfaces the failure as a denial the way
// decideAndExecute's own "refused before claim" path does for every other
// pre-execution refusal. It is a best-effort no-op, not an error, if the
// envelope is no longer Executed (e.g. concurrently moved on by something
// else) — the caller's own error is what matters, not this cleanup.
func (q *Queue) RevertClaim(ctx context.Context, id, reason string) {
	_, _ = q.st.TransitionApproval(ctx, id, string(Executed), string(Approved), reason, q.Now().UTC())
}

// Abandon ends an Approved envelope that could not be executed (the gate
// refused it before claiming it) as Denied, with reason on the record, so
// it reaches a clear final state instead of sitting Approved with nothing
// left that will ever run it. It is a no-op error if the envelope is no
// longer Approved.
func (q *Queue) Abandon(ctx context.Context, id, reason string) (Envelope, error) {
	e, err := q.Get(ctx, id)
	if err != nil {
		return Envelope{}, err
	}
	ok, err := q.st.TransitionApproval(ctx, id, string(Approved), string(Denied), reason, q.Now().UTC())
	if err != nil {
		return Envelope{}, err
	}
	if !ok {
		return e, fmt.Errorf("%w: %s is %s", ErrNotApproved, id, e.Status)
	}
	if _, err := q.log.Append(audit.Record{Kind: audit.KindDenial, Function: e.Action, EnvelopeID: id, Origin: e.Origin, Reason: reason, ArgsHash: e.PayloadHash}); err != nil {
		return Envelope{}, err
	}
	return q.Get(ctx, id)
}

func (q *Queue) getAfter(ctx context.Context, id string, cause error) (Envelope, error) {
	e, err := q.Get(ctx, id)
	if err != nil {
		return Envelope{}, err
	}
	return e, cause
}
