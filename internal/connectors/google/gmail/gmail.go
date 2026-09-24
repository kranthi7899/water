// Package gmail is the Gmail connector: it lists and reads the CEO's mail,
// and drafts/sends mail as the agent, through the shared gapi HTTP client.
// Every message list_messages/get_message return was written by someone
// else, so both are External; draft_message/send_message originate their
// own content, so neither is.
package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
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
// point at a fake server. mailAddress is the agent's verified "Send mail
// as" alias (config's agent.mail_address): draft_message/send_message
// always set MIME From: to this address, never the primary account's own,
// and never anything an args map supplies (there is no "from" argument).
type Gmail struct {
	opts        *gapi.Options
	mailAddress string
}

func New(mailAddress string) *Gmail { return &Gmail{mailAddress: mailAddress} }

// NewWithOptions builds a Gmail connector against test doubles.
func NewWithOptions(mailAddress string, o *gapi.Options) *Gmail {
	return &Gmail{mailAddress: mailAddress, opts: o}
}

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
		{
			Name:        "draft_message",
			Description: "Create a Gmail draft from the agent alias. Nothing is sent.",
			Level:       twins.D,
			Risk:        connectors.RiskLow,
			External:    false,
			Schema:      writeMessageSchema,
		},
		{
			Name:        "send_message",
			Description: "Send a Gmail message from the agent alias. This has an external effect and cannot be undone.",
			Level:       twins.A,
			Risk:        connectors.RiskHigh,
			External:    false,
			Schema:      writeMessageSchema,
		},
	}
}

// writeMessageSchema is shared by draft_message and send_message: there is
// deliberately no "from" property, so no args map can ever set it -- the
// From address is always g.mailAddress, read from config, never from a
// caller.
var writeMessageSchema = connectors.Schema{
	Properties: map[string]connectors.Property{
		"to":              {Type: "array", Items: &connectors.Property{Type: "string"}, Description: "recipient email addresses"},
		"subject":         {Type: "string", Description: "subject line"},
		"body":            {Type: "string", Description: "plain-text body"},
		"html_attachment": {Type: "string", Description: "optional pre-rendered HTML alternative body"},
	},
	Required: []string{"to", "subject", "body"},
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

// listMessagesOutput is list_messages' JSON output: the messages plus a
// history ID a caller can persist as the next incremental-sync cursor
// (gmail:history_id). On the incremental path it never lies past a message
// that was not returned (see listMessagesSinceHistory).
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
	case "draft_message":
		return g.draftMessage(ctx, cl, v.Args)
	case "send_message":
		return g.sendMessage(ctx, cl, v.Args)
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

// skipLabels are the labels whose messages the incremental path drops
// before fetching them: messages.list (the query path this replaces) leaves
// SPAM and TRASH out by default, and DRAFT autosaves are the CEO's own
// unsent writing, each save a fresh messageAdded record.
var skipLabels = map[string]bool{"SPAM": true, "TRASH": true, "DRAFT": true}

// listMessagesSinceHistory serves list_messages when since_history_id is
// set: it walks users.history.list for messageAdded records instead of
// searching, then reuses fetchMetadata for each newly-added message ID so
// the output/record shape matches the query-based path exactly.
//
// The returned history_id is the next call's cursor, so it must never move
// past a message this call did not return. The walk stops once max
// messages are collected (on a history-record boundary, so a record is
// never half-consumed and the output may exceed max by the rest of that one
// record) or after maxPages pages; in either case the cursor is the id of
// the last history record actually processed, and the next call resumes
// right after it. Only a walk that reached the end of the history uses the
// mailbox's latest historyId.
func (g *Gmail) listMessagesSinceHistory(ctx context.Context, cl *gapi.Client, sinceHistoryID string, max int) (json.RawMessage, error) {
	var ids []idPair
	seen := map[string]bool{}
	latest := ""  // mailbox's current historyId, from history.list itself
	lastRec := "" // id of the last history record processed
	complete := false
	pageToken := ""
walk:
	for page := 0; page < maxPages; page++ {
		q := url.Values{"startHistoryId": {sinceHistoryID}, "historyTypes": {"messageAdded"}}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		var resp struct {
			History []struct {
				ID            string `json:"id"`
				MessagesAdded []struct {
					Message struct {
						ID       string   `json:"id"`
						ThreadID string   `json:"threadId"`
						LabelIDs []string `json:"labelIds"`
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
		latest = laterHistoryID(latest, resp.HistoryID)
		for i, rec := range resp.History {
			for _, a := range rec.MessagesAdded {
				if a.Message.ID == "" || seen[a.Message.ID] || hasSkipLabel(a.Message.LabelIDs) {
					continue
				}
				seen[a.Message.ID] = true
				ids = append(ids, idPair{a.Message.ID, a.Message.ThreadID})
			}
			lastRec = laterHistoryID(lastRec, rec.ID)
			if len(ids) >= max {
				// Complete only if nothing at all is left after this record.
				complete = i == len(resp.History)-1 && resp.NextPageToken == ""
				break walk
			}
		}
		if resp.NextPageToken == "" {
			complete = true
			break
		}
		pageToken = resp.NextPageToken
	}

	out := make([]message, 0, len(ids))
	historyID := lastRec
	if complete {
		historyID = latest
	}
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
		if complete {
			// A message's own historyId can postdate records this walk
			// never reached, so it may only advance a complete walk's cursor.
			historyID = laterHistoryID(historyID, mHistoryID)
		}
	}
	return json.Marshal(listMessagesOutput{Messages: out, HistoryID: historyID})
}

func hasSkipLabel(labels []string) bool {
	for _, l := range labels {
		if skipLabels[l] {
			return true
		}
	}
	return false
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

// writeMessageOutput is draft_message/send_message's JSON output: the ids
// Gmail assigned plus the content that was actually built, since Gmail's
// create/send responses don't echo the message back. Normalize reads this
// to index what the agent said, not what the API returned.
type writeMessageOutput struct {
	ID       string   `json:"id"`
	ThreadID string   `json:"thread_id,omitempty"`
	DraftID  string   `json:"draft_id,omitempty"`
	From     string   `json:"from"`
	To       []string `json:"to"`
	Subject  string   `json:"subject"`
	Body     string   `json:"body"`
}

func (g *Gmail) draftMessage(ctx context.Context, cl *gapi.Client, args map[string]any) (json.RawMessage, error) {
	to, subject, body, html, err := g.readMessageArgs(args)
	if err != nil {
		return nil, err
	}
	raw, err := buildRawMessage(g.mailAddress, to, subject, body, html)
	if err != nil {
		return nil, err
	}
	var resp struct {
		ID      string `json:"id"`
		Message struct {
			ID       string `json:"id"`
			ThreadID string `json:"threadId"`
		} `json:"message"`
	}
	payload := map[string]any{"message": map[string]any{"raw": raw}}
	if err := cl.PostJSON(ctx, gapi.GmailBase+"/users/me/drafts", nil, payload, &resp); err != nil {
		return nil, err
	}
	return json.Marshal(writeMessageOutput{ID: resp.Message.ID, ThreadID: resp.Message.ThreadID, DraftID: resp.ID, From: g.mailAddress, To: to, Subject: subject, Body: body})
}

// sendMessage sends exactly once through gapi.PostJSON. A returned
// gapi.ErrSendOutcomeUnknown is passed straight back, never swallowed into
// a generic error, so a caller can errors.Is it and surface "uncertain,
// check Sent folder" instead of assuming success or retrying on its own.
func (g *Gmail) sendMessage(ctx context.Context, cl *gapi.Client, args map[string]any) (json.RawMessage, error) {
	to, subject, body, html, err := g.readMessageArgs(args)
	if err != nil {
		return nil, err
	}
	raw, err := buildRawMessage(g.mailAddress, to, subject, body, html)
	if err != nil {
		return nil, err
	}
	var resp struct {
		ID       string `json:"id"`
		ThreadID string `json:"threadId"`
	}
	payload := map[string]any{"raw": raw}
	if err := cl.PostJSON(ctx, gapi.GmailBase+"/users/me/messages/send", nil, payload, &resp); err != nil {
		return nil, err
	}
	return json.Marshal(writeMessageOutput{ID: resp.ID, ThreadID: resp.ThreadID, From: g.mailAddress, To: to, Subject: subject, Body: body})
}

// readMessageArgs reads to/subject/body/html_attachment from args. There is
// no "from" argument to read: the schema declares none, and even a caller
// that smuggled one into args (bypassing schema validation) would be
// ignored here -- the From address is always g.mailAddress, the CEO's
// configured agent alias, set once at connect time, not per call.
func (g *Gmail) readMessageArgs(args map[string]any) (to []string, subject, body, html string, err error) {
	if g.mailAddress == "" {
		return nil, "", "", "", errors.New("gmail: agent.mail_address is not configured; verify the agent's Gmail alias and set it before sending")
	}
	to = argStrings(args, "to")
	if len(to) == 0 {
		return nil, "", "", "", errors.New("gmail: to is required")
	}
	return to, gapi.ArgString(args, "subject"), gapi.ArgString(args, "body"), gapi.ArgString(args, "html_attachment"), nil
}

func argStrings(args map[string]any, key string) []string {
	switch v := args[key].(type) {
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return v
	}
	return nil
}

// sanitizeHeaderValue strips CR/LF so no argument can inject an extra
// header or smuggle content past the blank line ending the header block.
func sanitizeHeaderValue(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

// buildRawMessage builds an RFC 2822 message with a fixed From, base64url
// encoded as Gmail's drafts.create/messages.send "raw" field wants. When
// html is empty the message is a single text/plain part; otherwise it is
// multipart/alternative with body as the plain part and html as the html
// part, so a client with no HTML rendering still shows the plain text.
func buildRawMessage(from string, to []string, subject, body, html string) (string, error) {
	var head bytes.Buffer
	hdr := func(k, v string) { fmt.Fprintf(&head, "%s: %s\r\n", k, sanitizeHeaderValue(v)) }
	hdr("From", from)
	hdr("To", strings.Join(to, ", "))
	hdr("Subject", mime.QEncoding.Encode("UTF-8", subject))
	head.WriteString("MIME-Version: 1.0\r\n")

	if html == "" {
		head.WriteString("Content-Type: text/plain; charset=\"UTF-8\"\r\n")
		head.WriteString("Content-Transfer-Encoding: 8bit\r\n\r\n")
		head.WriteString(body)
		return base64.URLEncoding.EncodeToString(head.Bytes()), nil
	}

	var parts bytes.Buffer
	mw := multipart.NewWriter(&parts)
	plainPart, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {`text/plain; charset="UTF-8"`},
		"Content-Transfer-Encoding": {"8bit"},
	})
	if err != nil {
		return "", fmt.Errorf("gmail: building message: %s", err)
	}
	if _, err := plainPart.Write([]byte(body)); err != nil {
		return "", fmt.Errorf("gmail: building message: %s", err)
	}
	htmlPart, err := mw.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {`text/html; charset="UTF-8"`},
		"Content-Transfer-Encoding": {"8bit"},
	})
	if err != nil {
		return "", fmt.Errorf("gmail: building message: %s", err)
	}
	if _, err := htmlPart.Write([]byte(html)); err != nil {
		return "", fmt.Errorf("gmail: building message: %s", err)
	}
	if err := mw.Close(); err != nil {
		return "", fmt.Errorf("gmail: building message: %s", err)
	}
	fmt.Fprintf(&head, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", mw.Boundary())
	head.Write(parts.Bytes())
	return base64.URLEncoding.EncodeToString(head.Bytes()), nil
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
			out = append(out, toRecord(m, false))
		}
		return out, nil
	case "get_message":
		var m message
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, err
		}
		return []store.Record{toRecord(m, true)}, nil
	case "send_message":
		var w writeMessageOutput
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, err
		}
		m := message{ID: w.ID, ThreadID: w.ThreadID, From: w.From, To: w.To, Subject: w.Subject, Body: w.Body, InternalDate: strconv.FormatInt(time.Now().UnixMilli(), 10)}
		// Stays External: a sent body can quote someone else's mail
		// verbatim (agentmail's forwards do), so it is not clean content.
		return []store.Record{toRecord(m, true)}, nil
	}
	return nil, nil
}

// toRecord builds the stored message. full is true only for get_message,
// whose Body is the extracted message text; list_messages only has the
// snippet, and store.Upsert will not let that overwrite a full body.
func toRecord(m message, full bool) store.Record {
	return &store.Message{
		Meta:     store.Meta{Source: connName, SourceID: m.ID, External: true},
		Channel:  "email",
		Thread:   m.ThreadID,
		From:     m.From,
		To:       m.To,
		Subject:  m.Subject,
		Body:     m.Body,
		BodyFull: full,
		SentAt:   parseInternalDate(m.InternalDate),
	}
}
