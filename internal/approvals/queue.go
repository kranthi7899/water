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
type Envelope struct {
	ID           string
	Action       string // "connector.function"
	Recipient    string
	Payload      map[string]any
	EvidenceRefs []string
	Risk         string
	Origin       string
	ExpiresAt    time.Time

	PayloadHash string
	Status      Status
	Reason      string
	CreatedAt   time.Time
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

type Queue struct {
	st  *store.Store
	log *audit.Log
	Now func() time.Time

	// beforeTransition, when set (tests only), runs in Decide between the
	// pending check and the compare-and-swap, to interleave a racing decider.
	beforeTransition func()
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

// Propose adds a pending envelope.
func (q *Queue) Propose(ctx context.Context, e Envelope) (Envelope, error) {
	if e.Action == "" || e.Origin == "" {
		return Envelope{}, errors.New("approvals: action and origin are required")
	}
	now := q.Now().UTC()
	if e.ExpiresAt.IsZero() {
		e.ExpiresAt = now.Add(DefaultTTL)
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
		EvidenceRefs: e.EvidenceRefs, Risk: e.Risk, Origin: e.Origin, Status: string(Pending), CreatedAt: now, ExpiresAt: e.ExpiresAt.UTC()}
	if err := q.st.InsertApproval(ctx, row); err != nil {
		return Envelope{}, err
	}
	return q.Get(ctx, e.ID)
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
	return fromRow(row)
}

func fromRow(r store.ApprovalRow) (Envelope, error) {
	e := Envelope{ID: r.ID, Action: r.Action, Recipient: r.Recipient, EvidenceRefs: r.EvidenceRefs, Risk: r.Risk, Origin: r.Origin,
		ExpiresAt: r.ExpiresAt, PayloadHash: r.PayloadHash, Status: Status(r.Status), Reason: r.Reason, CreatedAt: r.CreatedAt}
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
		out = append(out, e)
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
		return Envelope{}, fmt.Errorf("approvals: %s changed while deciding", id)
	}
	if _, err := q.log.Append(audit.Record{Kind: audit.KindApproval, Function: e.Action, EnvelopeID: id, Origin: e.Origin, Allowed: true, Reason: "answered yes", ArgsHash: e.PayloadHash}); err != nil {
		// An approval that is not on the record must not stand.
		_, _ = q.st.TransitionApproval(ctx, id, string(Approved), string(Denied), "audit write failed", now)
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

func (q *Queue) getAfter(ctx context.Context, id string, cause error) (Envelope, error) {
	e, err := q.Get(ctx, id)
	if err != nil {
		return Envelope{}, err
	}
	return e, cause
}
