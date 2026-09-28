package memory

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Type is a record's kind. The set is closed: anything else is rejected.
type Type string

const (
	Preference  Type = "preference"
	Pattern     Type = "pattern"
	Entity      Type = "entity"
	Procedure   Type = "procedure"
	Commitment  Type = "commitment"
	Decision    Type = "decision"
	ContextNote Type = "context_note"
)

// Types is the closed set of record types.
var Types = []Type{Preference, Pattern, Entity, Procedure, Commitment, Decision, ContextNote}

// Valid reports whether t is in the closed set.
func (t Type) Valid() bool {
	for _, v := range Types {
		if t == v {
			return true
		}
	}
	return false
}

// Trigger is what caused a record to be written. docs/CONTEXT.md principle 6:
// long-term memory is written only from explicit CEO statements and
// approved corrections.
type Trigger string

const (
	CEOStatement       Trigger = "ceo_statement"
	ApprovedCorrection Trigger = "approved_correction"
	ApprovedProposal   Trigger = "approved_proposal"
)

// Valid reports whether t is a known trigger.
func (t Trigger) Valid() bool {
	return t == CEOStatement || t == ApprovedCorrection || t == ApprovedProposal
}

// Authors of a record's text (Provenance.WrittenBy).
const (
	AuthorCEO  = "ceo"
	AuthorTwin = "twin"
)

// Record is one long-term memory fact: a distilled statement plus a
// reference to where it came from, never the source content itself.
// Records are immutable once written; the only mutation is setting
// Invalidation, once.
type Record struct {
	ID          string // "mem_" + random hex; assigned by the store
	Type        Type
	Statement   string // the distilled fact, one or a few sentences
	Sensitivity Sensitivity
	// Subjects are optional typed refs for search ("person:<roster id>",
	// "project:<id>"): refs only, never names-plus-content.
	Subjects   []string
	Provenance Provenance
	Time       TimeBounds
	// Confidence is 0..1 and informational only. Owner decision
	// (2026-09-25): stored, no behavior. Nothing filters or ranks on it.
	Confidence   float64
	Supersedes   string        // id of the record this one corrects, or ""
	Invalidation *Invalidation // nil while live; set exactly once, never cleared
}

// Provenance says where a record came from and who stands behind it.
type Provenance struct {
	Trigger Trigger
	// SourceRef is a citation ("gmail:msg-abc123", "audit:1842",
	// "turn:<id>"): <scheme>:<id>, no whitespace, short. Never content.
	SourceRef string
	// AuditSeq is the audit.Entry.Seq of the event that produced this
	// write. F checks it is positive; G binds it to the live audit log.
	AuditSeq   int64
	WrittenBy  string // AuthorCEO or AuthorTwin
	ApprovedBy string // required unless the CEO wrote a ceo_statement
}

// TimeBounds place a fact in time.
type TimeBounds struct {
	ObservedAt  time.Time  // required: when the fact was observed or stated
	ValidFrom   time.Time  // defaults to ObservedAt
	ValidUntil  *time.Time // nil = open-ended
	ReviewAfter *time.Time // nil = no scheduled review
}

// Invalidation retires a record. It is set once and never cleared.
type Invalidation struct {
	At           time.Time
	By           string // "ceo", or the approver
	Reason       string
	AuditSeq     int64
	SupersededBy string // "" when invalidated without a replacement; set by the store
}

// Shape limits for refs. A body pasted into a ref fails on shape alone.
const (
	MaxSourceRefLen = 256
	MaxSubjectLen   = 128
	MaxSubjects     = 32
	MaxActorLen     = 64 // WrittenBy, ApprovedBy, Invalidation.By
)

// refRe is <scheme>:<id>: a lowercase scheme, a colon, then an id with no
// whitespace. RE2's \S is ASCII-only, so the id excludes every Unicode
// separator (\p{Z}: no-break spaces, line and paragraph separators) and
// every control or format character (\p{C}: \v, NUL, zero-width spaces);
// otherwise a sentence could ride in a ref with its spaces swapped out.
var refRe = regexp.MustCompile(`^[a-z][a-z0-9_.+-]{0,31}:[^\p{Z}\p{C}]+$`)

// ValidRef reports whether s is a well-formed <scheme>:<id> reference no
// longer than max bytes.
func ValidRef(s string, max int) bool {
	return len(s) <= max && utf8.ValidString(s) && refRe.MatchString(s)
}

// ErrInvalidRecord wraps every Validate failure.
var ErrInvalidRecord = errors.New("invalid memory record")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidRecord, fmt.Sprintf(format, a...))
}

// Validate checks everything about r that does not need the store: the
// closed sets, provenance (principle 6 included), time bounds and
// confidence. Whether Supersedes names a live record is checked by the
// store, inside the write transaction. Error messages name the field,
// never echo its content.
func (r Record) Validate() error {
	if !r.Type.Valid() {
		return invalid("unknown type")
	}
	if !r.Sensitivity.Valid() {
		return invalid("unknown sensitivity")
	}
	if strings.TrimSpace(r.Statement) == "" {
		return invalid("statement is empty")
	}
	if !utf8.ValidString(r.Statement) {
		return invalid("statement is not valid UTF-8")
	}
	if len(r.Subjects) > MaxSubjects {
		return invalid("more than %d subjects", MaxSubjects)
	}
	for i, s := range r.Subjects {
		if !ValidRef(s, MaxSubjectLen) {
			return invalid("subject %d is not a <kind>:<id> ref of at most %d bytes", i, MaxSubjectLen)
		}
	}
	if err := r.Provenance.validate(); err != nil {
		return err
	}
	if err := r.Time.validate(); err != nil {
		return err
	}
	if math.IsNaN(r.Confidence) || r.Confidence < 0 || r.Confidence > 1 {
		return invalid("confidence must be within [0, 1]")
	}
	if r.Supersedes != "" && !idRe.MatchString(r.Supersedes) {
		return invalid("supersedes is not a record id")
	}
	if r.Invalidation != nil {
		if err := r.Invalidation.Validate(); err != nil {
			return err
		}
		if s := r.Invalidation.SupersededBy; s != "" && !idRe.MatchString(s) {
			return invalid("invalidation superseded_by is not a record id")
		}
	}
	return nil
}

func (p Provenance) validate() error {
	if !p.Trigger.Valid() {
		return invalid("unknown provenance trigger")
	}
	if p.SourceRef == "" {
		return invalid("provenance source_ref is empty")
	}
	if !ValidRef(p.SourceRef, MaxSourceRefLen) {
		return invalid("provenance source_ref is not a <scheme>:<id> ref of at most %d bytes", MaxSourceRefLen)
	}
	if p.AuditSeq <= 0 {
		return invalid("provenance audit_seq must be positive")
	}
	if p.WrittenBy != AuthorCEO && p.WrittenBy != AuthorTwin {
		return invalid("provenance written_by must be %q or %q", AuthorCEO, AuthorTwin)
	}
	if p.Trigger == CEOStatement && p.WrittenBy != AuthorCEO {
		// A ceo_statement is the CEO's own words. A twin's paraphrase of
		// one is a proposal, which needs an approver.
		return invalid("a ceo_statement must be written by %q", AuthorCEO)
	}
	// Principle 6 in code: anything but the CEO's own statement names an
	// approver.
	if p.Trigger != CEOStatement && strings.TrimSpace(p.ApprovedBy) == "" {
		return invalid("provenance approved_by is required for trigger %s", p.Trigger)
	}
	if len(p.ApprovedBy) > MaxActorLen {
		return invalid("provenance approved_by is longer than %d bytes", MaxActorLen)
	}
	if isTwin(p.ApprovedBy) {
		// The twin approving its own text is no approval at all.
		return invalid("provenance approved_by cannot be %q", AuthorTwin)
	}
	return nil
}

// isTwin reports whether an actor field names the twin itself.
func isTwin(actor string) bool {
	return strings.EqualFold(strings.TrimSpace(actor), AuthorTwin)
}

// Start is the effective ValidFrom: ValidFrom, or ObservedAt when unset.
func (b TimeBounds) Start() time.Time {
	if b.ValidFrom.IsZero() {
		return b.ObservedAt
	}
	return b.ValidFrom
}

func (b TimeBounds) validate() error {
	if b.ObservedAt.IsZero() {
		return invalid("observed_at is required")
	}
	for _, t := range []*time.Time{&b.ObservedAt, &b.ValidFrom, b.ValidUntil, b.ReviewAfter} {
		if t != nil && !t.IsZero() && !inRange(*t) {
			return invalid("a time is outside %d-%d", minYear, maxYear)
		}
	}
	start := b.Start()
	if b.ValidUntil != nil && !b.ValidUntil.After(start) {
		return invalid("valid_until must be after valid_from")
	}
	if b.ReviewAfter != nil && b.ReviewAfter.Before(start) {
		return invalid("review_after must not be before valid_from")
	}
	return nil
}

// Validate checks an invalidation's own fields. SupersededBy is not
// checked here: the store sets it.
func (inv Invalidation) Validate() error {
	if inv.At.IsZero() {
		return invalid("invalidation at is required")
	}
	if !inRange(inv.At) {
		return invalid("invalidation at is outside %d-%d", minYear, maxYear)
	}
	if strings.TrimSpace(inv.By) == "" {
		return invalid("invalidation by is required")
	}
	if len(inv.By) > MaxActorLen {
		return invalid("invalidation by is longer than %d bytes", MaxActorLen)
	}
	if isTwin(inv.By) {
		// By is "ceo" or the approver; the twin retiring a fact on its own
		// is the same self-approval Provenance refuses.
		return invalid("invalidation by cannot be %q", AuthorTwin)
	}
	if strings.TrimSpace(inv.Reason) == "" {
		return invalid("invalidation reason is required")
	}
	if inv.AuditSeq <= 0 {
		return invalid("invalidation audit_seq must be positive")
	}
	return nil
}

// Stored times must round-trip through Unix nanoseconds (1678-2262).
const (
	minYear = 1900
	maxYear = 2200
)

func inRange(t time.Time) bool {
	y := t.UTC().Year()
	return y >= minYear && y <= maxYear
}

// IsCurrent is the one definition of a current record: not invalidated,
// ValidFrom <= now, and ValidUntil unset or later than now. Superseded
// records always carry an Invalidation, so "not superseded" needs no
// separate check. ReviewAfter never hides a record.
func IsCurrent(r Record, now time.Time) bool {
	if r.Invalidation != nil {
		return false
	}
	if r.Time.Start().After(now) {
		return false
	}
	return r.Time.ValidUntil == nil || r.Time.ValidUntil.After(now)
}
