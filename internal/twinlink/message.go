// Package twinlink is Slice E's twin-to-twin transport: one twin's daemon
// sending a message to a different twin's daemon (possibly another person's
// agent) and receiving one back.
//
// The trust model is the existing one, not a new one:
//
//   - Outbound is an outward action like any other. twinlink.send_message is
//     a level-A connector function, so it only ever runs through the gate
//     with an approved envelope from the existing approval queue, and the
//     human hears a code-built read-back of the exact message first.
//   - Inbound is untrusted external content, unconditionally, like meeting
//     speech and inbound mail. A received message is stored (external,
//     pinned by the table's CHECK), escalates the daemon's session taint,
//     and is only ever read — through twininbox.list_messages (level R,
//     External) or the CLI. Nothing a message says is ever run.
//
// The scope is deliberately narrow: two known twins, one request and at most
// one response to it (the database enforces that), plus one-way notices. No
// negotiation, no multi-hop routing, no forwarding.
package twinlink

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"water/internal/canon"
)

// Type is a message's kind.
type Type string

const (
	Request  Type = "request"
	Response Type = "response"
	Notice   Type = "notice"
)

func (t Type) valid() bool { return t == Request || t == Response || t == Notice }

// Message is the twin-to-twin envelope that crosses between daemons.
//
// InReplyTo is the one field beyond the original spec's envelope: a
// response has to say which request it answers, or "one request, one
// response" could not be enforced at all.
type Message struct {
	ID                 string     `json:"id"`
	FromTwin           string     `json:"from_twin"`
	ToTwin             string     `json:"to_twin"`
	Type               Type       `json:"type"`
	InReplyTo          string     `json:"in_reply_to,omitempty"`
	Subject            string     `json:"subject"`
	Payload            string     `json:"payload"`
	EvidenceRefs       []string   `json:"evidence_refs,omitempty"`
	NeedsHumanApproval bool       `json:"needs_human_approval"`
	ReplyBy            *time.Time `json:"reply_by,omitempty"`
	SentAt             time.Time  `json:"sent_at"`
}

// Limits on what one message may carry. A twin message is a short note
// between two assistants, not a file transfer.
const (
	MaxSubject      = 200      // runes
	MaxPayload      = 16 << 10 // bytes
	MaxEvidenceRefs = 20
	MaxEvidenceRef  = 500 // bytes
	// MaxWireBody bounds a whole POSTed message on the receiving side.
	MaxWireBody = 64 << 10
)

var (
	twinIDRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	msgIDRe  = regexp.MustCompile(`^tl_[0-9a-f]{32}$`)
)

// ValidTwinID reports whether id is a well-formed twin id (the same shape
// as a twins/<id> directory name).
func ValidTwinID(id string) bool { return twinIDRe.MatchString(id) }

// ValidMessageID reports whether id has DeriveID's shape.
func ValidMessageID(id string) bool { return msgIDRe.MatchString(id) }

// ErrInvalid wraps every Validate failure.
var ErrInvalid = errors.New("twinlink: invalid message")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Validate checks m's shape. It says nothing about whether m should be
// trusted — no inbound message ever is.
func (m Message) Validate() error {
	if !ValidMessageID(m.ID) {
		return invalid("id %q is malformed", m.ID)
	}
	if !ValidTwinID(m.FromTwin) || !ValidTwinID(m.ToTwin) {
		return invalid("from_twin and to_twin must be twin ids")
	}
	if m.FromTwin == m.ToTwin {
		return invalid("a twin does not message itself")
	}
	if !m.Type.valid() {
		return invalid("type must be request, response or notice")
	}
	switch {
	case m.Type == Response && !ValidMessageID(m.InReplyTo):
		return invalid("a response needs in_reply_to naming the request it answers")
	case m.Type != Response && m.InReplyTo != "":
		return invalid("only a response has in_reply_to")
	case m.Type != Request && m.ReplyBy != nil:
		return invalid("only a request has reply_by")
	}
	if strings.TrimSpace(m.Subject) == "" || utf8.RuneCountInString(m.Subject) > MaxSubject {
		return invalid("subject must be 1-%d characters", MaxSubject)
	}
	if strings.TrimSpace(m.Payload) == "" || len(m.Payload) > MaxPayload {
		return invalid("payload must be 1-%d bytes", MaxPayload)
	}
	if !utf8.ValidString(m.Subject) || !utf8.ValidString(m.Payload) {
		return invalid("subject and payload must be UTF-8 text")
	}
	if len(m.EvidenceRefs) > MaxEvidenceRefs {
		return invalid("at most %d evidence_refs", MaxEvidenceRefs)
	}
	for _, r := range m.EvidenceRefs {
		if r == "" || len(r) > MaxEvidenceRef || !utf8.ValidString(r) {
			return invalid("each evidence_ref must be 1-%d bytes of text", MaxEvidenceRef)
		}
	}
	if m.SentAt.IsZero() {
		return invalid("sent_at is required")
	}
	return nil
}

// content is the part of a message its id and content hash bind: everything
// except SentAt, which differs between two deliveries of the same message.
func (m Message) content() map[string]any {
	c := map[string]any{
		"from_twin": m.FromTwin, "to_twin": m.ToTwin, "type": string(m.Type), "in_reply_to": m.InReplyTo,
		"subject": m.Subject, "payload": m.Payload, "evidence_refs": nonNil(m.EvidenceRefs),
		"needs_human_approval": m.NeedsHumanApproval, "reply_by": "",
	}
	if m.ReplyBy != nil {
		c["reply_by"] = m.ReplyBy.UTC().Format(time.RFC3339)
	}
	return c
}

// ContentHash is the sha256 of m's canonical content (not its id or send
// time): a receiver uses it to tell an identical re-delivery from a
// conflicting reuse of one id.
func (m Message) ContentHash() (string, error) { return canon.Hash(m.content()) }

// DeriveID is m's id: a hash of its sender and content. The same approved
// message always gets the same id, so a re-delivery after an ambiguous
// network failure is recognized by the receiver as a duplicate rather than
// recorded twice.
func DeriveID(m Message) (string, error) {
	h, err := m.ContentHash()
	if err != nil {
		return "", err
	}
	return "tl_" + h[:32], nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
