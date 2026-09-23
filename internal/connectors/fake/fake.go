// Package fake provides in-memory calendar, mail and docs connectors that
// exercise the whole gate path without any network or account.
package fake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"water/internal/connectors"
	"water/internal/gate/permit"
	"water/internal/store"
	"water/internal/twins"
)

// MailService and MailAccount name the vault entry the fake mail connector requires, so
// credential handling is exercised end to end.
const (
	MailService = "water.fake_mail"
	MailAccount = "ceo"
)

func str(p string) connectors.Property { return connectors.Property{Type: "string", Description: p} }

var stringList = connectors.Property{Type: "array", Items: &connectors.Property{Type: "string"}}

func arg(args map[string]any, k string) string { s, _ := args[k].(string); return s }

func argList(args map[string]any, k string) []string {
	raw, _ := args[k].([]any)
	var out []string
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// ---- calendar ----

type Event struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Start     string   `json:"start"`
	End       string   `json:"end,omitempty"`
	Attendees []string `json:"attendees,omitempty"`
}

type Calendar struct {
	mu     sync.Mutex
	events []Event
}

func NewCalendar(seed ...Event) *Calendar { return &Calendar{events: seed} }

func (*Calendar) Name() string                 { return "fake_calendar" }
func (*Calendar) Credential() (string, string) { return "", "" }

func (*Calendar) Functions() []connectors.Function {
	return []connectors.Function{
		{Name: "list_events", Description: "List calendar events.", Level: twins.R, Risk: connectors.RiskLow},
		{Name: "create_event", Description: "Create an event and invite attendees.", Level: twins.A, Risk: connectors.RiskMedium,
			Schema: connectors.Schema{
				Properties: map[string]connectors.Property{"title": str("title"), "start": str("RFC 3339 start"), "end": str("RFC 3339 end"), "attendees": stringList},
				Required:   []string{"title", "start"},
			}},
	}
}

// Events returns a copy of the calendar.
func (c *Calendar) Events() []Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Event(nil), c.events...)
}

func (c *Calendar) Invoke(_ context.Context, p permit.Permit) (json.RawMessage, error) {
	call, err := p.Open()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	switch call.Function {
	case "list_events":
		return json.Marshal(c.events)
	case "create_event":
		e := Event{ID: fmt.Sprintf("ev%d", len(c.events)+1), Title: arg(call.Args, "title"), Start: arg(call.Args, "start"), End: arg(call.Args, "end"), Attendees: argList(call.Args, "attendees")}
		c.events = append(c.events, e)
		return json.Marshal(e)
	}
	return nil, fmt.Errorf("fake_calendar: unknown function %q", call.Function)
}

func (c *Calendar) Normalize(fn string, raw json.RawMessage) ([]store.Record, error) {
	var evs []Event
	switch fn {
	case "list_events":
		if err := json.Unmarshal(raw, &evs); err != nil {
			return nil, err
		}
	case "create_event":
		var e Event
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		evs = []Event{e}
	default:
		return nil, nil
	}
	var out []store.Record
	for _, e := range evs {
		start, _ := time.Parse(time.RFC3339, e.Start)
		end, _ := time.Parse(time.RFC3339, e.End)
		out = append(out, &store.Event{Meta: store.Meta{Source: c.Name(), SourceID: e.ID}, Title: e.Title, StartAt: start.UTC(), EndAt: end.UTC(), Attendees: e.Attendees})
	}
	return out, nil
}

// ---- mail ----

type Message struct {
	ID        string   `json:"id"`
	From      string   `json:"from"`
	To        []string `json:"to"`
	Subject   string   `json:"subject"`
	Body      string   `json:"body"`
	InReplyTo string   `json:"in_reply_to,omitempty"`
}

type Mail struct {
	mu    sync.Mutex
	inbox []Message
	sent  []Message
}

func NewMail(inbox ...Message) *Mail { return &Mail{inbox: inbox} }

func (*Mail) Name() string                 { return "fake_mail" }
func (*Mail) Credential() (string, string) { return MailService, MailAccount }

func (*Mail) Functions() []connectors.Function {
	return []connectors.Function{
		{Name: "list_messages", Description: "List inbox messages.", Level: twins.R, Risk: connectors.RiskLow, External: true},
		{Name: "draft_reply", Description: "Draft a reply to a message. Nothing is sent.", Level: twins.D, Risk: connectors.RiskLow,
			Schema: connectors.Schema{Properties: map[string]connectors.Property{"message_id": str("message to reply to"), "body": str("reply text")}, Required: []string{"message_id", "body"}}},
		{Name: "send_email", Description: "Send an email.", Level: twins.A, Risk: connectors.RiskHigh,
			Schema: connectors.Schema{
				Properties: map[string]connectors.Property{"to": stringList, "subject": str("subject"), "body": str("body"), "in_reply_to": str("message id")},
				Required:   []string{"to", "subject", "body"},
			}},
	}
}

// Sent returns what has left the fake mailbox.
func (m *Mail) Sent() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Message(nil), m.sent...)
}

func (m *Mail) Invoke(_ context.Context, p permit.Permit) (json.RawMessage, error) {
	call, err := p.Open()
	if err != nil {
		return nil, err
	}
	if call.Credential.IsZero() {
		return nil, errors.New("fake_mail: not authenticated")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch call.Function {
	case "list_messages":
		return json.Marshal(m.inbox)
	case "draft_reply":
		id := arg(call.Args, "message_id")
		for _, msg := range m.inbox {
			if msg.ID == id {
				subject := msg.Subject
				if !strings.HasPrefix(subject, "Re: ") {
					subject = "Re: " + subject
				}
				return json.Marshal(Message{To: []string{msg.From}, Subject: subject, Body: arg(call.Args, "body"), InReplyTo: id})
			}
		}
		return nil, fmt.Errorf("fake_mail: no message %q", id)
	case "send_email":
		msg := Message{ID: fmt.Sprintf("sent%d", len(m.sent)+1), From: MailAccount, To: argList(call.Args, "to"), Subject: arg(call.Args, "subject"), Body: arg(call.Args, "body"), InReplyTo: arg(call.Args, "in_reply_to")}
		m.sent = append(m.sent, msg)
		return json.Marshal(msg)
	}
	return nil, fmt.Errorf("fake_mail: unknown function %q", call.Function)
}

func (m *Mail) Normalize(fn string, raw json.RawMessage) ([]store.Record, error) {
	var msgs []Message
	external := true
	switch fn {
	case "list_messages":
		if err := json.Unmarshal(raw, &msgs); err != nil {
			return nil, err
		}
	case "send_email":
		var msg Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			return nil, err
		}
		msgs, external = []Message{msg}, false
	default:
		return nil, nil
	}
	var out []store.Record
	for _, msg := range msgs {
		out = append(out, &store.Message{Meta: store.Meta{Source: m.Name(), SourceID: msg.ID, External: external},
			Channel: "email", Thread: msg.InReplyTo, From: msg.From, To: msg.To, Subject: msg.Subject, Body: msg.Body})
	}
	return out, nil
}

// ---- docs ----

type Doc struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

type Docs struct {
	mu   sync.Mutex
	docs map[string]Doc
}

func NewDocs(seed ...Doc) *Docs {
	d := &Docs{docs: map[string]Doc{}}
	for _, doc := range seed {
		d.docs[doc.ID] = doc
	}
	return d
}

func (*Docs) Name() string                 { return "fake_docs" }
func (*Docs) Credential() (string, string) { return "", "" }

func (*Docs) Functions() []connectors.Function {
	return []connectors.Function{
		{Name: "read_doc", Description: "Read a document.", Level: twins.R, Risk: connectors.RiskLow, External: true,
			Schema: connectors.Schema{Properties: map[string]connectors.Property{"id": str("document id")}, Required: []string{"id"}}},
	}
}

func (d *Docs) Invoke(_ context.Context, p permit.Permit) (json.RawMessage, error) {
	call, err := p.Open()
	if err != nil {
		return nil, err
	}
	if call.Function != "read_doc" {
		return nil, fmt.Errorf("fake_docs: unknown function %q", call.Function)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	doc, ok := d.docs[arg(call.Args, "id")]
	if !ok {
		return nil, fmt.Errorf("fake_docs: no document %q", arg(call.Args, "id"))
	}
	return json.Marshal(doc)
}

func (d *Docs) Normalize(fn string, raw json.RawMessage) ([]store.Record, error) {
	if fn != "read_doc" {
		return nil, nil
	}
	var doc Doc
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	excerpt := []rune(doc.Body)
	if len(excerpt) > 280 {
		excerpt = excerpt[:280]
	}
	return []store.Record{&store.Document{Meta: store.Meta{Source: d.Name(), SourceID: doc.ID, External: true}, Title: doc.Title, Excerpt: string(excerpt), MimeType: "text/plain"}}, nil
}
