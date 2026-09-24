// Package gmail is the read-only Gmail connector: it lists and reads the
// CEO's mail through the shared gapi HTTP client. Every message it returns
// was written by someone else, so both its functions are External.
package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"water/internal/connectors"
	"water/internal/connectors/google/gapi"
	"water/internal/gate/permit"
	"water/internal/store"
	"water/internal/twins"
)

const connName = "gmail"

// maxBodyBytes caps a message body pulled from get_message.
const maxBodyBytes = 20 << 10

// maxPages bounds list_messages pagination regardless of what a server says,
// so a misbehaving nextPageToken can't loop forever. It applies to both the
// query-based messages.list path and the incremental history.list path.
const maxPages = 20

// ErrHistoryTooOld is returned when since_history_id names a history record
// Gmail no longer has (history.list answers 404). The caller should retry
// list_messages without since_history_id to get a full resync and a fresh
// history_id to resume from next time.
var ErrHistoryTooOld = errors.New("gmail: history too old, full resync needed")

// Gmail is the connector. opts is nil in production and set in tests to
// point at a fake server.
type Gmail struct{ opts *gapi.Options }

func New() *Gmail { return &Gmail{} }

// NewWithOptions builds a Gmail connector against test doubles.
func NewWithOptions(o *gapi.Options) *Gmail { return &Gmail{opts: o} }

func (*Gmail) Name() string                 { return connName }
func (*Gmail) Credential() (string, string) { return gapi.Service, gapi.DefaultAccount }

func (*Gmail) Functions() []connectors.Function {
	return []connectors.Function{
		{
			Name:        "list_messages",
			Description: "List Gmail messages matching a search query, newest first.",
			Level:       twins.R,
			Risk:        connectors.RiskLow,
			External:    true,
			Schema: connectors.Schema{
				Properties: map[string]connectors.Property{
					"query":            {Type: "string", Description: "Gmail search syntax, e.g. \"from:dana newer_than:1d\""},
					"max":              {Type: "integer", Description: "max messages to return (default 20, cap 100)"},
					"since_history_id": {Type: "string", Description: "optional: resume from this Gmail historyId instead of query, for incremental sync"},
				},
			},
		},
		{
			Name:        "get_message",
			Description: "Get one Gmail message's full body.",
			Level:       twins.R,
			Risk:        connectors.RiskLow,
			External:    true,
			Schema: connectors.Schema{
				Properties: map[string]connectors.Property{"id": {Type: "string", Description: "message id"}},
				Required:   []string{"id"},
			},
		},
	}
}

// message is the JSON shape Invoke returns and Normalize reads back, shared
// by both functions. list_messages fills Body with the snippet; get_message
// fills it with the extracted body.
type message struct {
	ID           string   `json:"id"`
	ThreadID     string   `json:"threadId"`
	From         string   `json:"from"`
	To           []string `json:"to"`
	Subject      string   `json:"subject"`
	Body         string   `json:"body"`
	InternalDate string   `json:"internalDate"`
}

type header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type mimePart struct {
	MimeType string   `json:"mimeType"`
	Headers  []header `json:"headers"`
	Body     struct {
		Data string `json:"data"`
	} `json:"body"`
	Parts []mimePart `json:"parts"`
}

type gmailMessage struct {
	ID           string   `json:"id"`
	ThreadID     string   `json:"threadId"`
	Snippet      string   `json:"snippet"`
	InternalDate string   `json:"internalDate"`
	HistoryID    string   `json:"historyId"`
	Payload      mimePart `json:"payload"`
}

// idPair is a message ID paired with its thread ID, as returned by both
// messages.list and history.list before either's per-message metadata has
// been fetched.
type idPair struct{ id, thread string }

// listMessagesOutput is list_messages' JSON output: the messages plus the
// mailbox's latest known history ID, so a caller can persist it as the next
// incremental-sync cursor (gmail:history_id).
type listMessagesOutput struct {
	Messages  []message `json:"messages"`
	HistoryID string    `json:"history_id,omitempty"`
}

func (g *Gmail) Invoke(ctx context.Context, p permit.Permit) (json.RawMessage, error) {
	v, err := p.Open()
	if err != nil {
		return nil, err
	}
	cl, err := gapi.FromSecret(v.Credential, g.opts)
	if err != nil {
		return nil, err
	}
	switch v.Function {
	case "list_messages":
		return g.listMessages(ctx, cl, v.Args)
	case "get_message":
		return g.getMessage(ctx, cl, v.Args)
	}
	return nil, fmt.Errorf("gmail: unknown function %q", v.Function)
}

func (g *Gmail) listMessages(ctx context.Context, cl *gapi.Client, args map[string]any) (json.RawMessage, error) {
	max, err := gapi.ArgInt(args, "max", 20, 1, 100)
	if err != nil {
		return nil, err
	}
	if since := gapi.ArgString(args, "since_history_id"); since != "" {
		return g.listMessagesSinceHistory(ctx, cl, since, max)
	}
	return g.listMessagesByQuery(ctx, cl, args, max)
}

// listMessagesByQuery is the original query-based search: unchanged from
// before since_history_id existed, aside from wrapping its result in
// listMessagesOutput and reporting the highest historyId seen along the way
// (messages.list itself carries no historyId field to read).
func (g *Gmail) listMessagesByQuery(ctx context.Context, cl *gapi.Client, args map[string]any, max int) (json.RawMessage, error) {
	query := gapi.ArgString(args, "query")

	var ids []idPair
	pageToken := ""
	for page := 0; len(ids) < max && page < maxPages; page++ {
		q := url.Values{"maxResults": {strconv.Itoa(max - len(ids))}}
		if query != "" {
			q.Set("q", query)
		}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		var resp struct {
			Messages []struct {
				ID       string `json:"id"`
				ThreadID string `json:"threadId"`
			} `json:"messages"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := cl.GetJSON(ctx, gapi.GmailBase+"/users/me/messages", q, &resp); err != nil {
			return nil, err
		}
		for _, m := range resp.Messages {
			ids = append(ids, idPair{m.ID, m.ThreadID})
			if len(ids) >= max {
				break
			}
		}
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}

	out := make([]message, 0, len(ids))
	historyID := ""
	for _, id := range ids {
		m, mHistoryID, err := fetchMetadata(ctx, cl, id.id)
		if err != nil {
			return nil, err
		}
		if m.ThreadID == "" {
			m.ThreadID = id.thread
		}
		out = append(out, m)
		historyID = laterHistoryID(historyID, mHistoryID)
	}
	return json.Marshal(listMessagesOutput{Messages: out, HistoryID: historyID})
}

// listMessagesSinceHistory serves list_messages when since_history_id is
// set: it walks users.history.list for messageAdded records instead of
// searching, then reuses fetchMetadata for each newly-added message ID so
// the output/record shape matches the query-based path exactly.
func (g *Gmail) listMessagesSinceHistory(ctx context.Context, cl *gapi.Client, sinceHistoryID string, max int) (json.RawMessage, error) {
	var ids []idPair
	seen := map[string]bool{}
	historyID := ""
	pageToken := ""
	for page := 0; page < maxPages; page++ {
		q := url.Values{"startHistoryId": {sinceHistoryID}, "historyTypes": {"messageAdded"}}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		var resp struct {
			History []struct {
				MessagesAdded []struct {
					Message struct {
						ID       string `json:"id"`
						ThreadID string `json:"threadId"`
					} `json:"message"`
				} `json:"messagesAdded"`
			} `json:"history"`
			NextPageToken string `json:"nextPageToken"`
			HistoryID     string `json:"historyId"`
		}
		if err := cl.GetJSON(ctx, gapi.GmailBase+"/users/me/history", q, &resp); err != nil {
			if gapi.Status(err) == http.StatusNotFound {
				return nil, fmt.Errorf("gmail: %w", ErrHistoryTooOld)
			}
			return nil, err
		}
		for _, rec := range resp.History {
			for _, a := range rec.MessagesAdded {
				if a.Message.ID == "" || seen[a.Message.ID] {
					continue
				}
				seen[a.Message.ID] = true
				ids = append(ids, idPair{a.Message.ID, a.Message.ThreadID})
			}
		}
		historyID = laterHistoryID(historyID, resp.HistoryID)
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	if len(ids) > max {
		ids = ids[:max]
	}

	out := make([]message, 0, len(ids))
	for _, id := range ids {
		m, mHistoryID, err := fetchMetadata(ctx, cl, id.id)
		if err != nil {
			// A message added-then-deleted between history.list and this
			// fetch is a normal race, not the stale-cursor condition: skip
			// it rather than failing the whole call.
			if gapi.Status(err) == http.StatusNotFound {
				continue
			}
			return nil, err
		}
		if m.ThreadID == "" {
			m.ThreadID = id.thread
		}
		out = append(out, m)
		historyID = laterHistoryID(historyID, mHistoryID)
	}
	return json.Marshal(listMessagesOutput{Messages: out, HistoryID: historyID})
}

func fetchMetadata(ctx context.Context, cl *gapi.Client, id string) (message, string, error) {
	var raw gmailMessage
	q := url.Values{"format": {"metadata"}, "metadataHeaders": {"From", "To", "Subject", "Date"}}
	if err := cl.GetJSON(ctx, gapi.GmailBase+"/users/me/messages/"+url.PathEscape(id), q, &raw); err != nil {
		return message{}, "", err
	}
	from, to, subject := headerValues(raw.Payload.Headers)
	m := message{ID: raw.ID, ThreadID: raw.ThreadID, From: from, To: splitAddrs(to), Subject: subject, Body: capBody(raw.Snippet), InternalDate: raw.InternalDate}
	return m, raw.HistoryID, nil
}

// laterHistoryID returns whichever of a, b is the more recent Gmail history
// ID, treating "" as absent. History IDs are decimal strings that increase
// over time; unparseable or differently-sized values fall back to a length,
// then lexical, comparison, which still holds for any all-digit string.
func laterHistoryID(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	ai, aerr := strconv.ParseUint(a, 10, 64)
	bi, berr := strconv.ParseUint(b, 10, 64)
	if aerr == nil && berr == nil {
		if bi > ai {
			return b
		}
		return a
	}
	if len(a) != len(b) {
		if len(b) > len(a) {
			return b
		}
		return a
	}
	if b > a {
		return b
	}
	return a
}

func (g *Gmail) getMessage(ctx context.Context, cl *gapi.Client, args map[string]any) (json.RawMessage, error) {
	id := gapi.ArgString(args, "id")
	if id == "" {
		return nil, errors.New("gmail: id is required")
	}
	var raw gmailMessage
	q := url.Values{"format": {"full"}}
	if err := cl.GetJSON(ctx, gapi.GmailBase+"/users/me/messages/"+url.PathEscape(id), q, &raw); err != nil {
		return nil, err
	}
	from, to, subject := headerValues(raw.Payload.Headers)
	body := capBody(extractBody(raw.Payload))
	m := message{ID: raw.ID, ThreadID: raw.ThreadID, From: from, To: splitAddrs(to), Subject: subject, Body: body, InternalDate: raw.InternalDate}
	return json.Marshal(m)
}

func headerValues(hs []header) (from, to, subject string) {
	for _, h := range hs {
		switch strings.ToLower(h.Name) {
		case "from":
			from = h.Value
		case "to":
			to = h.Value
		case "subject":
			subject = h.Value
		}
	}
	return
}

func splitAddrs(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// extractBody walks the MIME tree for the first text/plain part, falling
// back to the first text/html part stripped of tags.
func extractBody(p mimePart) string {
	var plain, htmlPart string
	walkParts(p, &plain, &htmlPart)
	if plain != "" {
		return decodeB64URL(plain)
	}
	if htmlPart != "" {
		return stripHTML(decodeB64URL(htmlPart))
	}
	return ""
}

func walkParts(p mimePart, plain, htmlPart *string) {
	switch p.MimeType {
	case "text/plain":
		if *plain == "" && p.Body.Data != "" {
			*plain = p.Body.Data
		}
	case "text/html":
		if *htmlPart == "" && p.Body.Data != "" {
			*htmlPart = p.Body.Data
		}
	}
	for _, part := range p.Parts {
		walkParts(part, plain, htmlPart)
	}
}

// decodeB64URL decodes Gmail's base64url body data, which may or may not be
// padded.
func decodeB64URL(s string) string {
	s = strings.TrimRight(s, "=")
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return ""
	}
	return string(b)
}

var tagPattern = regexp.MustCompile(`<[^>]*>`)

func stripHTML(s string) string {
	s = tagPattern.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	return strings.Join(strings.Fields(s), " ")
}

func capBody(s string) string {
	if len(s) <= maxBodyBytes {
		return s
	}
	return strings.ToValidUTF8(s[:maxBodyBytes], "")
}

func parseInternalDate(s string) time.Time {
	ms, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func (*Gmail) Normalize(fn string, raw json.RawMessage) ([]store.Record, error) {
	switch fn {
	case "list_messages":
		var lm listMessagesOutput
		if err := json.Unmarshal(raw, &lm); err != nil {
			return nil, err
		}
		out := make([]store.Record, 0, len(lm.Messages))
		for _, m := range lm.Messages {
			out = append(out, toRecord(m))
		}
		return out, nil
	case "get_message":
		var m message
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return []store.Record{toRecord(m)}, nil
	}
	return nil, nil
}

func toRecord(m message) store.Record {
	return &store.Message{
		Meta:    store.Meta{Source: connName, SourceID: m.ID, External: true},
		Channel: "email",
		Thread:  m.ThreadID,
		From:    m.From,
		To:      m.To,
		Subject: m.Subject,
		Body:    m.Body,
		SentAt:  parseInternalDate(m.InternalDate),
	}
}
