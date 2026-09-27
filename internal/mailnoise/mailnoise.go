// Package mailnoise classifies one message's bulk-mail "noise" signal from
// general, connector-agnostic shape: bulk-mail headers and labels, sender
// shape, phishing-style claim/confirm phrasing and preheader padding. No
// domain, company or brand name is ever named in this code — every rule is
// a general signal, not a denylist of specific senders.
//
// This is a pure leaf: stdlib only, no water/internal imports, so both
// internal/decisions and internal/runtime can depend on it without either
// depending on the other (see internal/decisions/trigger.go's own note on
// why that import direction matters: internal/decisions cannot import
// internal/runtime, and internal/runtime already imports internal/decisions
// for the brief's open-cards signal). A leaf package sidesteps the question
// entirely.
//
// Verdict.Class ("noise"|"informative"|"") is this package's own, narrow
// signal about one message's bulk-mail shape. It is unrelated to Slice W's
// route_log.class column (internal/store's routing table, values
// "company"|"general"): the two share the English word "class" but name
// unrelated concepts — this one is about whether a message is bulk/
// promotional mail unlikely to need a decision, that one is about which
// conversational register a reply should use. Do not conflate or rename
// either from the other.
package mailnoise

import "strings"

// Signals is the subset of a message's headers/labels this package needs.
// The caller (internal/decisions, internal/runtime) builds it from whatever
// store type it holds; mailnoise itself never imports internal/store, so it
// stays a leaf and store never needs to know mailnoise exists.
type Signals struct {
	// Labels are the message's provider labels (Gmail's LabelIds, e.g.
	// "CATEGORY_PROMOTIONS").
	Labels []string
	// ListUnsubscribe, ListID, Precedence and AutoSubmitted are the raw
	// values of the message's List-Unsubscribe, List-Id, Precedence and
	// Auto-Submitted headers, or "" when absent.
	ListUnsubscribe string
	ListID          string
	Precedence      string
	AutoSubmitted   string
}

// Class values Verdict.Class can hold.
const (
	ClassNoise       = "noise"
	ClassInformative = "informative"
)

// Verdict is Classify's answer for one message.
type Verdict struct {
	// Class is "noise", "informative" or "" (no verdict; the caller's own
	// existing logic decides what to do with the message).
	Class string
	// Reasons names which signals fired, for debuggability only (never
	// shown to the model or the owner). Every entry is a fixed, generic
	// label — never the message's own sender, domain or subject text — so
	// Reasons can never leak or imply a hardcoded domain/company name.
	Reasons []string
}

// bulkLabels are the provider category labels the header/label rule treats
// as noise outright, regardless of anything else about the message.
var bulkLabels = map[string]bool{
	"CATEGORY_PROMOTIONS": true,
	"CATEGORY_UPDATES":    true,
	"CATEGORY_SOCIAL":     true,
	"CATEGORY_FORUMS":     true,
}

// bulkPrecedence are the Precedence header values (case-insensitive) the
// header/label rule treats as noise outright.
var bulkPrecedence = map[string]bool{"bulk": true, "list": true, "junk": true}

// bulkSenderMarkers is internal/decisions/attention.go's and
// internal/runtime/brief.go's own bulkSenderMarkers list (kept there,
// unchanged, for their own separate "needs attention" heuristic), extended
// with a few more bulk-shaped local parts per docs/slices/UI.md's Phase 0
// spec. Checked as a substring of the whole (lowercased) From address, the
// same way the original heuristic checks its own list.
var bulkSenderMarkers = []string{
	"no-reply", "noreply", "do-not-reply", "donotreply", "notifications@", "notification@",
	"mailer-daemon", "newsletter@",
	"updates@", "premium@", "news@", "marketing@", "info@", "hello@",
}

// bulkDomainSegments are whole, hyphen/dot-separated domain segments that
// mark a domain as bulk-mail-shaped (e.g. "x-mail.com", "mail.x.com").
// Matching is on whole segments only, never substrings, so a domain like
// "example.com" or "germaine.com" is never mistaken for one of these
// (docs/slices/UI.md's own example and counter-example).
var bulkDomainSegments = map[string]bool{"mail": true, "email": true, "news": true, "em": true}

// claimPhrases are case-insensitive phrases in a subject or snippet that
// read as a phishing-style "confirm this is you" hook.
var claimPhrases = []string{
	"is this your", "claim ", "confirm your", "did you write", "are you the", "add to profile",
}

// paddingChars are invisible/combining characters seen used to pad a
// preheader so a mail client's snippet preview shows nothing useful: word
// joiner, the zero-width space/non-joiner/joiner family, the zero-width
// no-break space (also the UTF-8 BOM), and soft hyphen.
var paddingChars = map[rune]bool{
	0x034f: true, // combining grapheme joiner
	0x200b: true, // zero width space
	0x200c: true, // zero width non-joiner
	0x200d: true, // zero width joiner
	0xfeff: true, // zero width no-break space / UTF-8 BOM
	0x00ad: true, // soft hyphen
}

// minPaddingRun is how many consecutive padding characters count as the
// preheader-padding signal.
const minPaddingRun = 5

// Classify returns from/subject/snippet's noise verdict. wroteTo reports
// whether the CEO has ever sent mail to an address at a domain
// (internal/store's SentToDomain backs this in production); nil is treated
// as always false, the conservative direction (never having written to a
// domain never turns a noise verdict into a false negative).
//
// Rules (docs/slices/UI.md Phase 0, U21):
//  1. A bulk-mail header or category label makes the message noise outright.
//  2. Otherwise, a claim/confirm phrase, together with a sender domain the
//     CEO has never written to, together with either a bulk-sender shape or
//     preheader padding, makes it noise.
//  3. Otherwise, a bulk-sender shape with no claim/confirm phrase makes it
//     informative.
//  4. Otherwise there is no verdict ("").
func Classify(from, subject, snippet string, sig Signals, wroteTo func(domain string) bool) Verdict {
	if wroteTo == nil {
		wroteTo = func(string) bool { return false }
	}

	var headerReasons []string
	if strings.TrimSpace(sig.ListUnsubscribe) != "" {
		headerReasons = append(headerReasons, "header: list-unsubscribe")
	}
	if strings.TrimSpace(sig.ListID) != "" {
		headerReasons = append(headerReasons, "header: list-id")
	}
	if bulkPrecedence[strings.ToLower(strings.TrimSpace(sig.Precedence))] {
		headerReasons = append(headerReasons, "header: precedence")
	}
	if as := strings.ToLower(strings.TrimSpace(sig.AutoSubmitted)); as != "" && as != "no" {
		headerReasons = append(headerReasons, "header: auto-submitted")
	}
	for _, l := range sig.Labels {
		if bulkLabels[strings.ToUpper(strings.TrimSpace(l))] {
			headerReasons = append(headerReasons, "label: bulk category")
			break
		}
	}
	if len(headerReasons) > 0 {
		return Verdict{Class: ClassNoise, Reasons: headerReasons}
	}

	_, domain := splitAddress(from)
	var reasons []string
	bulkShape := senderLooksBulk(from, domain)
	if bulkShape {
		reasons = append(reasons, "sender-shape")
	}
	claim := containsClaimPhrase(subject + " " + snippet)
	if claim {
		reasons = append(reasons, "claim-pattern")
	}
	padded := hasPaddingRun(snippet)
	if padded {
		reasons = append(reasons, "preheader-padding")
	}

	if claim && !wroteTo(domain) && (bulkShape || padded) {
		return Verdict{Class: ClassNoise, Reasons: reasons}
	}
	if bulkShape && !claim {
		return Verdict{Class: ClassInformative, Reasons: reasons}
	}
	return Verdict{Class: "", Reasons: reasons}
}

// senderLooksBulk reports the "bulk sender shape" signal: a marker in the
// whole From address, or a whole hyphen/dot-separated domain segment that
// names a bulk-mail-shaped word.
func senderLooksBulk(from, domain string) bool {
	lf := strings.ToLower(from)
	for _, marker := range bulkSenderMarkers {
		if strings.Contains(lf, marker) {
			return true
		}
	}
	for _, seg := range splitSegments(domain) {
		if bulkDomainSegments[seg] {
			return true
		}
	}
	return false
}

// splitSegments splits a domain on '.' and '-' into lowercased segments.
func splitSegments(domain string) []string {
	if domain == "" {
		return nil
	}
	return strings.FieldsFunc(domain, func(r rune) bool { return r == '.' || r == '-' })
}

func containsClaimPhrase(s string) bool {
	s = strings.ToLower(s)
	for _, p := range claimPhrases {
		if strings.Contains(s, p) {
			return true
		}
	}
	return false
}

// hasPaddingRun reports whether snippet contains a run of at least
// minPaddingRun consecutive padding characters.
func hasPaddingRun(snippet string) bool {
	run := 0
	for _, r := range snippet {
		if paddingChars[r] {
			run++
			if run >= minPaddingRun {
				return true
			}
			continue
		}
		run = 0
	}
	return false
}

// splitAddress returns addr's local part and domain, lowercased, tolerating
// a "Name <addr>" wrapper and surrounding whitespace. Both are "" when addr
// has no "@".
func splitAddress(addr string) (local, domain string) {
	addr = strings.TrimSpace(addr)
	if i := strings.LastIndex(addr, "<"); i >= 0 {
		if j := strings.Index(addr[i:], ">"); j >= 0 {
			addr = addr[i+1 : i+j]
		}
	}
	at := strings.LastIndex(addr, "@")
	if at < 0 {
		return "", ""
	}
	return strings.ToLower(strings.TrimSpace(addr[:at])), strings.ToLower(strings.TrimSpace(addr[at+1:]))
}
