package decisions

import (
	"fmt"
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

// fields are the values an args template may substitute.
func fields(r store.Record) map[string]string {
	f := map[string]string{"source_id": ""}
	if m := Meta(r); m != nil {
		f["source_id"] = m.SourceID
	}
	var subject string
	switch v := r.(type) {
	case *store.Message:
		f["sender"], f["thread"], subject = v.From, v.Thread, v.Subject
	case *store.Meeting:
		f["sender"], subject = v.Organizer, v.Title
	case *store.Event:
		f["sender"], subject = v.Organizer, v.Title
	case *store.Document:
		f["sender"], subject = v.Owner, v.Title
	case *store.Issue:
		subject = v.Title
	case *store.Commit:
		f["sender"], subject = v.Author, v.Message
	case *store.Transaction:
		subject = v.Counterparty
	case *store.Contact:
		f["sender"], subject = v.Email, v.Name
	}
	f["subject"] = subject
	f["keywords"] = keywords(subject)
	return f
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
