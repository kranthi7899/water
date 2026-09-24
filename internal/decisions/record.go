package decisions

import (
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"unicode"

	"water/internal/store"
)

// Meta returns a record's common metadata, or nil for an unknown type.
func Meta(r store.Record) *store.Meta {
	switch v := r.(type) {
	case *store.Message:
		return &v.Meta
	case *store.Meeting:
		return &v.Meta
	case *store.Event:
		return &v.Meta
	case *store.Document:
		return &v.Meta
	case *store.Issue:
		return &v.Meta
	case *store.Commit:
		return &v.Meta
	case *store.Transaction:
		return &v.Meta
	case *store.Contact:
		return &v.Meta
	}
	return nil
}

// Ref is a record's "source:source_id" identity, the form every Evidence
// source and SourceItemIDs entry uses. It is "" when the record has none.
func Ref(r store.Record) string {
	m := Meta(r)
	if m == nil || m.Source == "" || m.SourceID == "" {
		return ""
	}
	return m.Source + ":" + m.SourceID
}

func external(r store.Record) bool {
	m := Meta(r)
	return m != nil && m.External
}

// describe renders a record as one plain line for evidence and prompts.
func describe(r store.Record) string {
	switch v := r.(type) {
	case *store.Message:
		s := "Message from " + v.From
		if v.Subject != "" {
			s += ": " + v.Subject
		}
		if v.Body != "" {
			s += " — " + v.Body
		}
		return clip(s, 240)
	case *store.Meeting:
		return clip(fmt.Sprintf("Meeting %s at %s: %s", v.Title, stamp(v.StartAt), v.Summary), 240)
	case *store.Event:
		return clip(fmt.Sprintf("Event %s at %s", v.Title, stamp(v.StartAt)), 240)
	case *store.Document:
		s := "Document " + v.Title
		if v.Excerpt != "" {
			s += " — " + v.Excerpt
		}
		return clip(s, 240)
	case *store.Issue:
		return clip(fmt.Sprintf("Issue %s (%s)", v.Title, v.State), 240)
	case *store.Commit:
		return clip(fmt.Sprintf("Commit %s in %s: %s", v.SHA, v.Repo, v.Message), 240)
	case *store.Transaction:
		return clip(fmt.Sprintf("Transaction %s %d (minor units) %s: %s", v.Counterparty, v.AmountMinor, v.Currency, v.Description), 240)
	case *store.Contact:
		return clip(fmt.Sprintf("Contact %s <%s> %s", v.Name, v.Email, v.Org), 240)
	}
	return ""
}

// fields are the values an args template may substitute, plus
// "sender_display" (the raw sender, for code-built prose only; it is not a
// placeholder a template may use).
func fields(r store.Record) map[string]string {
	f := map[string]string{"source_id": ""}
	if m := Meta(r); m != nil {
		f["source_id"] = m.SourceID
	}
	var subject, sender string
	switch v := r.(type) {
	case *store.Message:
		sender, f["thread"], subject = v.From, v.Thread, v.Subject
	case *store.Meeting:
		sender, subject = v.Organizer, v.Title
	case *store.Event:
		sender, subject = v.Organizer, v.Title
	case *store.Document:
		sender, subject = v.Owner, v.Title
	case *store.Issue:
		subject = v.Title
	case *store.Commit:
		sender, subject = v.Author, v.Message
	case *store.Transaction:
		subject = v.Counterparty
	case *store.Contact:
		sender, subject = v.Email, v.Name
	}
	f["sender_display"] = sender
	f["sender"] = senderAddress(sender)
	f["subject"] = subject
	f["keywords"] = keywords(subject)
	return f
}

// addressRe is the only shape a {sender} value may take: a bare address
// with no quotes, spaces, parentheses, braces or search operators.
var addressRe = regexp.MustCompile(`^[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+$`)

// senderAddress reduces a raw From/Organizer/Owner value to its bare email
// address. The header is written by whoever sent the mail: a display name
// like `"a" OR "invoice"` would otherwise become Gmail search syntax in a
// "from:{sender}" query, and even an honest "Dana Smith <dana@x.com>" only
// binds from: to "Dana". Anything that does not reduce to a plain address
// is "", which fill() reports as missing so the need is skipped, not run
// with a guessed value.
func senderAddress(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	a, err := mail.ParseAddress(v)
	if err != nil || !addressRe.MatchString(a.Address) {
		return ""
	}
	return a.Address
}

var stopwords = map[string]bool{
	"re": true, "fw": true, "fwd": true, "the": true, "and": true, "for": true, "with": true, "your": true,
	"you": true, "our": true, "this": true, "that": true, "from": true, "about": true, "can": true,
	"could": true, "please": true, "need": true, "are": true, "was": true, "will": true, "have": true,
	"has": true, "into": true, "what": true, "when": true, "how": true, "urgent": true, "asap": true,
}

// keywords picks up to four distinctive words from a subject for a search
// query: letters and digits only, so nothing in it is search syntax.
func keywords(s string) string {
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if len([]rune(w)) < 3 || stopwords[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
		if len(out) == 4 {
			break
		}
	}
	return strings.Join(out, " ")
}
