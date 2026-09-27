// Package spokenemail reads email addresses out of spoken (transcribed)
// text and checks addresses a model is about to write to.
//
// Everything here is pure and uses only the standard library: no network, no
// state. Candidates never rewrites an utterance; its result is only a hint
// the turn prompt adds next to the CEO's own words (docs/slices/W.md D4b).
// CheckSyntax, NearMiss and CheckRecipient are the pure half of the
// recipient checks gmail's write functions and the approvals queue run
// before anything is drafted or proposed (D4c); the DNS half lives in
// internal/approvals.
package spokenemail

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// maxCandidates bounds Candidates' result.
const maxCandidates = 3

// knownProviders are the public mail providers a misheard domain is most
// likely to be a near-miss of, in the order NearMiss reports them. A few
// real providers that sit one edit away from these (mail.com, ymail.com,
// email.com) are listed so they match exactly instead of reading as typos.
var knownProviders = []string{
	"gmail.com", "googlemail.com", "outlook.com", "hotmail.com", "live.com",
	"yahoo.com", "ymail.com", "icloud.com", "me.com", "proton.me",
	"protonmail.com", "aol.com", "mail.com", "email.com", "gmx.com",
	"zoho.com", "rediffmail.com",
}

// KnownProviders returns a copy of the public mail providers NearMiss and
// SpellOut know by name.
func KnownProviders() []string { return append([]string(nil), knownProviders...) }

// realDomains are real mail domains that sit within NearMiss's reach of a
// known provider (a regional TLD of the same provider, or an unrelated
// company one or two edits away). They are never read as misheard, but they
// are not near-miss targets either.
var realDomains = map[string]bool{
	"protonmail.ch": true, "proton.ch": true, "yahoo.ca": true, "hotmail.ca": true,
	"outlook.cz": true, "outlook.cl": true, "email.cz": true,
	"cloud.com": true, "hotmart.com": true, "proton.ai": true,
}

func isKnownProvider(domain string) bool {
	for _, p := range knownProviders {
		if domain == p {
			return true
		}
	}
	return false
}

// atMarker matches the spoken forms of "@" that need no domain lookahead:
// the Indian-English "at the rate" and what speech recognition makes of it
// ("at the right", "at the red", ...), glued to what follows or not. Longer
// alternatives come first so "rate" wins over "rat".
var atMarker = regexp.MustCompile(`\bat\s+the\s*(?:rate|rat|right|rite|red|write)|\bat\s+rate\b`)

// joinedWords are spoken splits of provider names ("g mail") put back
// together before tokens are read.
var joinedWords = strings.NewReplacer(
	" g mail ", " gmail ", " hot mail ", " hotmail ", " out look ", " outlook ",
	" i cloud ", " icloud ", " proton mail ", " protonmail ", " yahoo mail ", " yahoo ",
)

// cueWords introduce an address ("send it to ...", "my email is ...").
var cueWords = map[string]bool{
	"to": true, "is": true, "address": true, "email": true, "mail": true,
	"id": true, "e-mail": true,
}

// emailContext words must appear somewhere in an utterance before a bare
// "at" is read as "@": "check the article at nytimes.com" is not an address.
var emailContext = map[string]bool{
	"email": true, "e-mail": true, "mail": true, "address": true, "send": true,
	"draft": true, "write": true, "reply": true, "forward": true, "cc": true,
	"contact": true, "message": true, "id": true, "gmail": true,
}

// stopLocal are ordinary words that are never read as a local part: "meet me
// at ..." is not me@.
var stopLocal = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true, "to": true, "of": true,
	"in": true, "on": true, "at": true, "is": true, "am": true, "are": true, "was": true,
	"be": true, "me": true, "you": true, "him": true, "her": true, "us": true, "them": true,
	"it": true, "i": true, "we": true, "they": true, "look": true, "meet": true, "here": true,
	"there": true, "that": true, "this": true, "work": true, "home": true,
	"s": true, "re": true, "ll": true, "t": true, "d": true, "m": true, "ve": true,
	"email": true, "mail": true, "address": true, "id": true, "my": true, "his": true,
	"their": true, "our": true, "your": true, "send": true, "draft": true, "write": true,
}

// Candidates returns up to three email addresses that utterance may have
// spoken, normalized, deduplicated and in the order heard; nil if it names
// none. "at the rate"/"at the right" and "at" (before something domain
// shaped) mean "@", "dot" ".", "underscore" "_", "dash"/"hyphen" "-", and
// runs of single spelled letters join up. Every candidate passes
// CheckSyntax. It is a hint only: it never changes the utterance.
func Candidates(utterance string) []string {
	toks := tokens(utterance)
	if len(toks) == 0 {
		return nil
	}
	ctx := false
	for _, t := range toks {
		if emailContext[t] {
			ctx = true
			break
		}
	}
	var out []string
	seen := map[string]bool{}
	add := func(a string) {
		if a == "" || seen[a] || len(out) >= maxCandidates || CheckSyntax(a) != nil {
			return
		}
		seen[a] = true
		out = append(out, a)
	}
	for i, t := range toks {
		if t != "@" && t != "at" {
			continue
		}
		domain := readDomain(toks[i+1:])
		if !strings.Contains(domain, ".") {
			continue
		}
		// A bare "at" is "@" only in an utterance about mail, or right
		// before a known provider ("john at outlook dot com").
		if t == "at" && !ctx && !isKnownProvider(domain) {
			continue
		}
		local, start, simple := readLocal(toks[:i])
		if local == "" || stopLocal[local] {
			continue
		}
		if t == "at" && stopLocal[toks[i-1]] {
			continue
		}
		// "kranti get a job at ..." heard as words: after a cue, join the
		// short last word with the 1-maxCueJoin words before it.
		if simple && len(local) <= 6 {
			if joined := joinAfterCue(toks[:start], local); joined != "" {
				add(joined + "@" + domain)
			}
		}
		add(local + "@" + domain)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// tokens lowercases s, turns the spoken symbol words and "at the rate" into
// symbols, joins runs of single letters, and splits on space. "@" is always
// its own token; "." "_" "-" are their own tokens when spoken.
func tokens(s string) []string {
	s = strings.ToLower(s)
	s = atMarker.ReplaceAllString(s, " @ ")
	s = strings.ReplaceAll(s, "@", " @ ")
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			return r
		case r == '@', r == '.', r == '_', r == '-', r == '+':
			return r
		}
		return ' '
	}, s)
	s = joinedWords.Replace(" " + strings.Join(strings.Fields(s), " ") + " ")
	var raw []string
	for _, f := range strings.Fields(s) {
		if len(f) > 1 {
			f = strings.TrimRight(f, ".")
			f = strings.TrimLeft(f, ".")
		}
		switch f {
		case "dot", "period":
			f = "."
		case "underscore":
			f = "_"
		case "dash", "hyphen":
			f = "-"
		case "":
			continue
		}
		raw = append(raw, f)
	}
	// Join runs of two or more single letters or digits: "k r a n t h i".
	var out []string
	for i := 0; i < len(raw); {
		if !single(raw[i]) {
			out = append(out, raw[i])
			i++
			continue
		}
		j := i
		var b strings.Builder
		for j < len(raw) && single(raw[j]) {
			b.WriteString(raw[j])
			j++
		}
		if j-i >= 2 {
			out = append(out, b.String())
		} else {
			out = append(out, raw[i])
		}
		i = j
	}
	return out
}

func single(t string) bool {
	return len(t) == 1 && (t[0] >= 'a' && t[0] <= 'z' || t[0] >= '0' && t[0] <= '9')
}

var labelish = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]*[a-z0-9])?$`)

// readDomain reads the domain after "@": a label, then any number of
// ("." or "-") label pairs. Two labels in a row with nothing spoken between
// them end it: the second word is no longer part of the address.
func readDomain(after []string) string {
	var b strings.Builder
	expectLabel := true
	for _, t := range after {
		if expectLabel {
			if !labelish.MatchString(t) {
				break
			}
			b.WriteString(t)
			expectLabel = false
			continue
		}
		if t == "." || t == "-" {
			b.WriteString(t)
			expectLabel = true
			continue
		}
		break
	}
	return strings.Trim(b.String(), ".-")
}

var localish = regexp.MustCompile(`^[a-z0-9+]([a-z0-9.+_-]*[a-z0-9+])?$`)

// readLocal reads the local part right before "@", walking back over
// ("." "_" "-") separators: "john _ doe" is john_doe. start is the index of
// its first token; simple is true when it is one word with no separators.
func readLocal(before []string) (local string, start int, simple bool) {
	i := len(before) - 1
	if i < 0 || !localish.MatchString(before[i]) {
		return "", 0, false
	}
	parts := []string{before[i]}
	start = i
	for i-2 >= 0 {
		sep := before[i-1]
		if sep != "." && sep != "_" && sep != "-" {
			break
		}
		if !localish.MatchString(before[i-2]) || stopLocal[before[i-2]] {
			break
		}
		parts = append([]string{before[i-2], sep}, parts...)
		i -= 2
		start = i
	}
	return strings.Join(parts, ""), start, len(parts) == 1
}

// maxCueJoin is how many purely alphabetic words joinAfterCue joins in
// front of the short last word: enough for a spelled initial plus a heard
// phrase ("to k kranti get a job"), few enough that ordinary sentences stay
// out.
const maxCueJoin = 4

// joinAfterCue joins the 1-maxCueJoin purely alphabetic words between a cue
// word (or the start of the utterance) and local: "to kranti get a job"
// gives "krantigetajob", "to k kranti get a job" gives "kkrantigetajob". It
// returns "" when there is no such run.
func joinAfterCue(before []string, local string) string {
	var words []string
	for i := len(before) - 1; i >= 0 && len(words) <= maxCueJoin; i-- {
		w := before[i]
		if cueWords[w] {
			if len(words) == 0 {
				return ""
			}
			return strings.Join(words, "") + local
		}
		if !isAlpha(w) || w == "at" {
			return ""
		}
		words = append([]string{w}, words...)
	}
	if len(words) >= 1 && len(words) <= maxCueJoin && len(words) == len(before) {
		return strings.Join(words, "") + local
	}
	return ""
}

func isAlpha(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// SpellOut reads addr aloud unambiguously: the local part letter by letter
// (digits as digits, "." "_" "-" "+" named), then "at", then the domain as
// words when it is a known provider ("gmail dot com"), otherwise each label
// letter by letter with common top-level domains read as words.
func SpellOut(addr string) string {
	addr = strings.ToLower(strings.TrimSpace(addr))
	at := strings.LastIndex(addr, "@")
	if at < 0 {
		return spell(addr)
	}
	local, domain := addr[:at], addr[at+1:]
	var dom string
	if isKnownProvider(domain) {
		dom = strings.ReplaceAll(domain, ".", " dot ")
	} else {
		labels := strings.Split(domain, ".")
		parts := make([]string, len(labels))
		for i, l := range labels {
			if i == len(labels)-1 && wordTLD[l] {
				parts[i] = l
			} else {
				parts[i] = spell(l)
			}
		}
		dom = strings.Join(parts, " dot ")
	}
	return spell(local) + " at " + dom
}

var wordTLD = map[string]bool{"com": true, "net": true, "org": true, "edu": true, "gov": true}

func spell(s string) string {
	parts := make([]string, 0, len(s))
	for _, r := range s {
		switch r {
		case '.':
			parts = append(parts, "dot")
		case '_':
			parts = append(parts, "underscore")
		case '-':
			parts = append(parts, "dash")
		case '+':
			parts = append(parts, "plus")
		default:
			parts = append(parts, string(r))
		}
	}
	return strings.Join(parts, " ")
}

var (
	localRe = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+/=?^_`{|}~-]+(\\.[A-Za-z0-9!#$%&'*+/=?^_`{|}~-]+)*$")
	labelRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	tldRe   = regexp.MustCompile(`^[A-Za-z]{2,63}$`)
)

// CheckSyntax is a pragmatic RFC 5322 subset: exactly one "@", a dot-atom
// local part of 1-64 characters, and a domain of at least two labels
// (letters, digits, inner hyphens) whose last is 2+ letters. It accepts no
// display name, quotes, comments or IP literals.
func CheckSyntax(addr string) error {
	bad := fmt.Errorf("%q is not a valid email address", addr)
	if strings.Count(addr, "@") != 1 {
		return bad
	}
	at := strings.IndexByte(addr, '@')
	local, domain := addr[:at], addr[at+1:]
	if len(local) < 1 || len(local) > 64 || !localRe.MatchString(local) {
		return bad
	}
	if len(domain) > 253 {
		return bad
	}
	labels := strings.Split(domain, ".")
	if len(labels) < 2 {
		return bad
	}
	for _, l := range labels {
		if !labelRe.MatchString(l) {
			return bad
		}
	}
	if !tldRe.MatchString(labels[len(labels)-1]) {
		return bad
	}
	return nil
}

// NearMiss reports whether domain looks like a mistake for one of known (a
// public provider or a company domain) and which one. domain is a near-miss
// of P when it is not P, not any other known domain and not a subdomain of
// one, and either:
//   - it ends in P with no "." before it (a word glued on: therightgmail.com);
//   - it has P's first label and a top-level part within 2 edits (gmail.co);
//   - it is within Damerau-Levenshtein distance 1 of P when P's first label
//     has 5 characters, or 2 when it has 6 or more (gmial.com, outlok.com).
//
// A known domain whose first label is 4 characters or fewer (me.com,
// aol.com, live.com) only ever matches exactly: one edit from those is too
// often a real, unrelated domain (ms.com, aon.com).
func NearMiss(domain string, known []string) (string, bool) {
	d := strings.ToLower(strings.TrimSpace(strings.TrimSuffix(domain, ".")))
	if d == "" || realDomains[d] {
		return "", false
	}
	norm := make([]string, 0, len(known))
	for _, k := range known {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == "" {
			continue
		}
		if d == k || strings.HasSuffix(d, "."+k) {
			return "", false
		}
		norm = append(norm, k)
	}
	for _, p := range norm {
		first, rest, _ := strings.Cut(p, ".")
		if len(first) <= 4 {
			continue
		}
		if strings.HasSuffix(d, p) {
			return p, true
		}
		dFirst, dRest, _ := strings.Cut(d, ".")
		if dFirst == first && dRest != "" && distance(dRest, rest) <= 2 {
			return p, true
		}
		limit := 1
		if len(first) >= 6 {
			limit = 2
		}
		if distance(d, p) <= limit {
			return p, true
		}
	}
	return "", false
}

// distance is the optimal-string-alignment (restricted Damerau-Levenshtein)
// edit distance: insertions, deletions, substitutions and adjacent
// transpositions each cost 1.
func distance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	n, m := len(ra), len(rb)
	d := make([][]int, n+1)
	for i := range d {
		d[i] = make([]int, m+1)
		d[i][0] = i
	}
	for j := 0; j <= m; j++ {
		d[0][j] = j
	}
	for i := 1; i <= n; i++ {
		for j := 1; j <= m; j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[n][m]
}

// ErrInvalid and ErrNearMiss classify CheckRecipient's refusals.
var (
	ErrInvalid  = errors.New("invalid recipient address")
	ErrNearMiss = errors.New("recipient domain looks misheard")
)

// Address returns the bare address in s, which may be "addr" or
// "Name <addr>".
func Address(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '<'); i >= 0 && strings.HasSuffix(s, ">") {
		return strings.TrimSpace(s[i+1 : len(s)-1])
	}
	return s
}

// CheckRecipient is the pure recipient check every gmail write runs, with
// wording the model can act on. known is the public providers plus any
// company domains. A syntax error is always refused (ErrInvalid). A
// near-miss domain is refused (ErrNearMiss) unless confirmed is true, the
// model's statement that the CEO confirmed this spelled-out address; then
// it comes back as a warning instead.
//
// addr is one recipient item: exactly one address. An item that carries a
// second "@" ("x@a.com, Name <y@b.com>", "x@a.com <y@b.com>") is refused as
// invalid, because Address reads only the bracketed one while the whole item
// goes into the To header, so the other address would skip every check.
func CheckRecipient(addr string, known []string, confirmed bool) (warning string, err error) {
	a := Address(addr)
	if strings.Count(addr, "@") != 1 {
		return "", fmt.Errorf("%w: %q is not one valid email address; give each recipient as its own item, read it back to the CEO spelled out and confirm it before trying again", ErrInvalid, strings.TrimSpace(addr))
	}
	if CheckSyntax(a) != nil {
		return "", fmt.Errorf("%w: %q is not a valid email address; read it back to the CEO spelled out and confirm it before trying again", ErrInvalid, a)
	}
	domain := strings.ToLower(a[strings.IndexByte(a, '@')+1:])
	p, near := NearMiss(domain, known)
	if !near {
		return "", nil
	}
	if confirmed {
		return fmt.Sprintf("Unusual domain %s (close to %s), confirmed in conversation", domain, p), nil
	}
	return "", fmt.Errorf("%w: %q looks like a misheard %q; read the address back to the CEO spelled out (%s) and confirm it. Only if the CEO confirms this exact address, retry with confirm_unusual_recipient set to true",
		ErrNearMiss, a, p, SpellOut(a))
}
