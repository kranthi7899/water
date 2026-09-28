package memory

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The never-store validator. The brief's list, verbatim: raw email/message
// bodies, raw conversation transcripts, credentials/tokens, payment details
// beyond a vault handle, government ID numbers.
//
// CheckNeverStore runs inside every Write and Supersede before any backend
// sees a record, and again on every read, so a hand-edited store holding a
// forbidden value fails to load rather than serving it.
//
// It is a floor, not a guarantee. Pattern detection has false negatives (a
// paraphrased email passes) and false positives (a digit run that happens
// to be Luhn-valid). The structural defense is the record's shape: a short
// statement plus a reference, with no field meant to hold content. This is
// also only one of two enforcements; the other is the boundary stated in
// the agent's own operating context, which is Slice G's.

// Category names what kind of forbidden content was found.
type Category string

const (
	CatCredential    Category = "credential"
	CatPayment       Category = "payment"
	CatGovernmentID  Category = "government_id"
	CatRawMessage    Category = "raw_message"
	CatRawTranscript Category = "raw_transcript"
)

// ErrNeverStore is the typed rejection. Its message names the category,
// field and rule, and never contains the rejected text: a logged error
// would otherwise defeat the rule.
type ErrNeverStore struct {
	Category Category
	Field    string // "statement", "subjects[2]", "provenance.source_ref", ...
	Rule     string // which check fired, e.g. "luhn-valid card number"
}

func (e ErrNeverStore) Error() string {
	return fmt.Sprintf("memory: refused to store %s content in %s (%s); store a short statement plus a source ref instead",
		e.Category, e.Field, e.Rule)
}

// PROVISIONAL thresholds (owner decision at F's Approve, 2026-09-25): set
// here, in one place, chosen to keep false positives low, and not
// calibrated against real records yet.
const (
	// MaxStatementRunes is the hard length cap on a Statement (and on an
	// invalidation Reason). A distilled fact is one or a few sentences;
	// 1000 runes is roughly 150 to 200 words.
	MaxStatementRunes = 1000
	// A run of token characters ([A-Za-z0-9_+=-], no '/', '.' or ':') is
	// treated as an unrecognized secret when it is at least
	// MinSecretTokenLen long, mixes upper case, lower case and digits, and
	// its Shannon entropy is at least MinSecretTokenEntropy bits per
	// character. Random base64 of that length scores about 4.9; English
	// words, hex hashes (no upper case) and ids like "ENG-1234" do not
	// qualify. Applied to free-text fields only: opaque provider ids in
	// refs (a Drive file id is 44 mixed-case characters) are high-entropy
	// by design, so refs get the known-shape checks instead.
	MinSecretTokenLen     = 40
	MinSecretTokenEntropy = 4.3
)

type field struct {
	name     string
	text     string
	freeText bool // statement-like: the length cap and entropy check apply
}

// CheckNeverStore checks every string field of r that could carry content:
// Statement, Subjects, the Provenance strings and the Invalidation strings.
// It returns the first ErrNeverStore found, or nil.
func CheckNeverStore(r Record) error {
	fields := []field{{"statement", r.Statement, true}}
	for i, s := range r.Subjects {
		fields = append(fields, field{fmt.Sprintf("subjects[%d]", i), s, false})
	}
	fields = append(fields,
		field{"provenance.source_ref", r.Provenance.SourceRef, false},
		field{"provenance.written_by", r.Provenance.WrittenBy, true},
		field{"provenance.approved_by", r.Provenance.ApprovedBy, true},
		field{"supersedes", r.Supersedes, false},
	)
	if r.Invalidation != nil {
		fields = append(fields, invalidationFields(*r.Invalidation)...)
	}
	for _, f := range fields {
		if err := checkField(f); err != nil {
			return err
		}
	}
	return nil
}

// CheckInvalidation runs the never-store check over an invalidation alone,
// before it is applied.
func CheckInvalidation(inv Invalidation) error {
	for _, f := range invalidationFields(inv) {
		if err := checkField(f); err != nil {
			return err
		}
	}
	return nil
}

func invalidationFields(inv Invalidation) []field {
	return []field{
		{"invalidation.by", inv.By, true},
		{"invalidation.reason", inv.Reason, true},
		{"invalidation.superseded_by", inv.SupersededBy, false},
	}
}

func checkField(f field) error {
	if f.text == "" {
		return nil
	}
	reject := func(c Category, rule string) error {
		return ErrNeverStore{Category: c, Field: f.name, Rule: rule}
	}
	// The pattern checks run over a normalized copy, so Unicode spaces,
	// dashes, zero-width characters, non-ASCII digits and non-\n line
	// breaks cannot disguise a forbidden value. The length cap counts the
	// text as given.
	s := normalizeForScan(f.text)
	if rule := credentialRule(s, f.freeText); rule != "" {
		return reject(CatCredential, rule)
	}
	if rule := paymentRule(s); rule != "" {
		return reject(CatPayment, rule)
	}
	if rule := governmentIDRule(s); rule != "" {
		return reject(CatGovernmentID, rule)
	}
	if rule := rawMessageRule(s); rule != "" {
		return reject(CatRawMessage, rule)
	}
	if f.freeText && utf8.RuneCountInString(f.text) > MaxStatementRunes {
		return reject(CatRawMessage, fmt.Sprintf("longer than %d characters", MaxStatementRunes))
	}
	if rule := transcriptRule(s); rule != "" {
		return reject(CatRawTranscript, rule)
	}
	return nil
}

// normalizeForScan folds the Unicode disguises a pattern would otherwise
// miss: CRLF, CR and the Unicode line breaks become \n (Go's (?m)^ only
// knows \n); other Unicode spaces become ' '; dash punctuation becomes
// '-'; format characters (zero-width spaces and joiners, soft hyphens,
// bidi controls) are dropped; full-width ASCII becomes ASCII; and every
// decimal digit becomes its ASCII digit.
func normalizeForScan(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == '\r':
			if i+1 < len(rs) && rs[i+1] == '\n' {
				continue
			}
			b.WriteByte('\n')
		case r == '\n' || r == '\v' || r == '\f' || r == 0x85 || r == 0x2028 || r == 0x2029:
			b.WriteByte('\n')
		case r == ' ' || r == '\t':
			b.WriteRune(r)
		case unicode.IsSpace(r) || unicode.Is(unicode.Zs, r):
			b.WriteByte(' ')
		case unicode.Is(unicode.Cf, r):
			// dropped
		case r >= 0xFF01 && r <= 0xFF5E:
			b.WriteRune(r - 0xFEE0)
		case r > unicode.MaxASCII && unicode.Is(unicode.Pd, r):
			b.WriteByte('-')
		case r > unicode.MaxASCII && unicode.IsDigit(r):
			b.WriteByte(byte('0' + digitValue(r)))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// digitValue is a Unicode decimal digit's value. Unicode guarantees every
// Nd character sits in a contiguous 0..9 run, so the offset from the start
// of its table range, mod 10, is the value.
func digitValue(r rune) int {
	for _, rg := range unicode.Nd.R16 {
		if lo, hi := rune(rg.Lo), rune(rg.Hi); r >= lo && r <= hi && rg.Stride == 1 {
			return int(r-lo) % 10
		}
	}
	for _, rg := range unicode.Nd.R32 {
		if lo, hi := rune(rg.Lo), rune(rg.Hi); r >= lo && r <= hi && rg.Stride == 1 {
			return int(r-lo) % 10
		}
	}
	return 0
}

// hashRe is a long hex-only token (a record id's hex, a Gmail message id,
// a commit hash or digest). One that contains a letter is an identifier,
// not a card or an SSN, so unglueDigits blanks it rather than splitting it
// into digit runs. The cost: a number glued to a prefix made only of the
// letters a-f ("cc4111...") stays hidden; "card4111..." does not.
var hashRe = regexp.MustCompile(`\b[0-9A-Fa-f]{16,}\b`)

// unglueDigits is the view the card and SSN checks scan: ASCII letters and
// '_' become spaces, so a number glued to a word ("visa4111111111111111",
// "ssn123-45-6789") still sits on a \b boundary. Long hex hashes are
// blanked first so their digit runs are not mistaken for numbers; '_'
// counts as a separator for that, so a record id's "mem_<hex>" is blanked.
func unglueDigits(s string) string {
	s = strings.ReplaceAll(s, "_", " ")
	s = hashRe.ReplaceAllStringFunc(s, func(m string) string {
		if strings.Trim(m, "0123456789") == "" {
			return m // all digits: exactly what the checks look for
		}
		return strings.Repeat(" ", len(m))
	})
	return strings.Map(func(r rune) rune {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			return ' '
		}
		return r
	}, s)
}

// --- credentials / tokens ---

var credentialShapes = []struct {
	re   *regexp.Regexp
	rule string
}{
	{regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*(?:KEY|KEY BLOCK)-----`), "PEM key block"},
	{regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9\-._~+/]{16,}=*`), "bearer token"},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), "JWT"},
	{regexp.MustCompile(`\bya29\.[A-Za-z0-9_-]{20,}`), "Google access token"},
	{regexp.MustCompile(`(?:^|[^A-Za-z0-9])1//[A-Za-z0-9_-]{20,}`), "Google refresh token"},
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`), "Google API key"},
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`), "sk- secret key"},
	{regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,})`), "GitHub token"},
	{regexp.MustCompile(`\bxox[abpres]-[A-Za-z0-9-]{10,}`), "Slack token"},
	{regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`), "AWS access key id"},
	{regexp.MustCompile(`(?i)\b(?:password|passwd|passcode|pwd)\s*[:=]\s*\S+`), "password assignment"},
}

var tokenRunRe = regexp.MustCompile(`[A-Za-z0-9_+=-]{` + strconv.Itoa(MinSecretTokenLen) + `,}`)

func credentialRule(s string, freeText bool) string {
	for _, c := range credentialShapes {
		if c.re.MatchString(s) {
			return c.rule
		}
	}
	if freeText {
		for _, tok := range tokenRunRe.FindAllString(s, -1) {
			if looksLikeSecret(tok) {
				return "high-entropy token"
			}
		}
	}
	return ""
}

func looksLikeSecret(tok string) bool {
	var upper, lower, digit bool
	for _, r := range tok {
		switch {
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsLower(r):
			lower = true
		case unicode.IsDigit(r):
			digit = true
		}
	}
	return upper && lower && digit && shannonEntropy(tok) >= MinSecretTokenEntropy
}

func shannonEntropy(s string) float64 {
	counts := map[rune]int{}
	n := 0
	for _, r := range s {
		counts[r]++
		n++
	}
	h := 0.0
	for _, c := range counts {
		p := float64(c) / float64(n)
		h -= p * math.Log2(p)
	}
	return h
}

// --- payment details beyond a vault handle ---

var (
	// Card-number shapes: a contiguous 13-19 digit run, groups of four
	// (16-19 digits), or Amex's 4-6-5. Mixed or arbitrary separators are
	// deliberately not matched, so dates and phone numbers written with
	// dashes or spaces do not look card-like.
	cardShapes = []*regexp.Regexp{
		regexp.MustCompile(`\b\d{13,19}\b`),
		regexp.MustCompile(`\b\d{4}(?:[ -]\d{4}){3}(?:[ -]?\d{1,3})?\b`),
		regexp.MustCompile(`\b\d{4}[ -]\d{6}[ -]\d{5}\b`),
	}
	cvvRe    = regexp.MustCompile(`(?i)\b(?:cvv2?|cvc2?|csc|security code|exp(?:iry|iration)?(?: date)?)\b`)
	expiryRe = regexp.MustCompile(`\b(?:0[1-9]|1[0-2])\s*/\s*(?:\d{2}|20\d{2})\b`)
	ibanRe   = regexp.MustCompile(`\b[A-Z]{2}\d{2}(?: ?[A-Z0-9]{4}){2,7}(?: ?[A-Z0-9]{1,4})?\b`)
	vaultRe  = regexp.MustCompile(`(?i)\bvault:(\S*)`)
	// vault:<service>/<account>, matching vault.Vault's (service, account)
	// addressing: both parts non-empty, neither starting with '-' (the
	// keychain backend's own rule), no further '/'.
	vaultHandleRe = regexp.MustCompile(`^[A-Za-z0-9_.@+][A-Za-z0-9_.@+-]*/[A-Za-z0-9_.@+][A-Za-z0-9_.@+-]*$`)
)

func paymentRule(s string) string {
	for _, m := range vaultRe.FindAllStringSubmatch(s, -1) {
		if !vaultHandleRe.MatchString(strings.TrimRight(m[1], `.,;:!?)"'`)) {
			return "malformed vault handle (want vault:<service>/<account>)"
		}
	}
	cardLike := false
	digits := unglueDigits(s)
	for _, re := range cardShapes {
		for _, m := range re.FindAllString(digits, -1) {
			d := digitsOnly(m)
			if len(d) < 13 || len(d) > 19 {
				continue
			}
			cardLike = true
			if luhnValid(d) {
				return "luhn-valid card number"
			}
		}
	}
	if cardLike && (cvvRe.MatchString(s) || expiryRe.MatchString(s)) {
		return "card-like number with CVV or expiry"
	}
	for _, m := range ibanRe.FindAllString(s, -1) {
		if ibanValid(strings.ReplaceAll(m, " ", "")) {
			return "IBAN"
		}
	}
	return ""
}

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func luhnValid(d string) bool {
	sum := 0
	double := false
	for i := len(d) - 1; i >= 0; i-- {
		n := int(d[i] - '0')
		if double {
			n *= 2
			if n > 9 {
				n -= 9
			}
		}
		sum += n
		double = !double
	}
	return sum%10 == 0
}

// ibanValid is the ISO 13616 mod-97 check.
func ibanValid(iban string) bool {
	if len(iban) < 15 || len(iban) > 34 {
		return false
	}
	rearranged := iban[4:] + iban[:4]
	rem := 0
	for _, r := range rearranged {
		var v int
		switch {
		case r >= '0' && r <= '9':
			v = int(r - '0')
		case r >= 'A' && r <= 'Z':
			v = int(r-'A') + 10
		default:
			return false
		}
		if v >= 10 {
			rem = (rem*100 + v) % 97
		} else {
			rem = (rem*10 + v) % 97
		}
	}
	return rem == 1
}

// --- government ID numbers ---
//
// PROVISIONAL (owner decision, 2026-09-25): US SSN only. Passport numbers,
// driver's licences and non-US national IDs are deferred; each added
// pattern raises false positives.

var (
	ssnDelimitedRe = regexp.MustCompile(`\b(\d{3})[- ](\d{2})[- ](\d{4})\b`)
	ssnBareRe      = regexp.MustCompile(`\b(\d{3})(\d{2})(\d{4})\b`)
)

func governmentIDRule(s string) string {
	s = unglueDigits(s)
	for _, re := range []*regexp.Regexp{ssnDelimitedRe, ssnBareRe} {
		for _, m := range re.FindAllStringSubmatch(s, -1) {
			if plausibleSSN(m[1], m[2], m[3]) {
				return "US SSN"
			}
		}
	}
	return ""
}

// plausibleSSN excludes the ranges SSA never issues (area 000, 666 and
// 900-999; group 00; serial 0000) to cut false positives.
func plausibleSSN(area, group, serial string) bool {
	a, _ := strconv.Atoi(area)
	if a == 0 || a == 666 || a >= 900 {
		return false
	}
	return group != "00" && serial != "0000"
}

// --- raw email / message bodies ---

var (
	headerLineRe    = regexp.MustCompile(`(?im)^[ \t]*(from|to|cc|bcc|subject|date|sent|reply-to|message-id|received|return-path|mime-version|content-type)[ \t]*:`)
	quotedLineRe    = regexp.MustCompile(`(?m)^[ \t]*>`)
	wroteRe         = regexp.MustCompile(`(?im)^[ \t>]*on\s.{1,200}\swrote:[ \t]*$`)
	originalMsgRe   = regexp.MustCompile(`(?i)-{2,}\s*(?:original message|forwarded message)\s*-{2,}`)
	sigDelimiterRe  = regexp.MustCompile(`(?m)^--[ ]?\r?\n\s*\S`)
	strongHeaderSet = map[string]bool{"message-id": true, "received": true, "return-path": true, "mime-version": true, "content-type": true}
)

func rawMessageRule(s string) string {
	headers := map[string]bool{}
	for _, m := range headerLineRe.FindAllStringSubmatch(s, -1) {
		h := strings.ToLower(m[1])
		if strongHeaderSet[h] {
			return "email header lines"
		}
		headers[h] = true
	}
	if len(headers) >= 2 {
		return "email header lines"
	}
	if originalMsgRe.MatchString(s) {
		return "forwarded/original message marker"
	}
	if len(quotedLineRe.FindAllStringIndex(s, -1)) >= 2 || wroteRe.MatchString(s) {
		return "quoted reply"
	}
	if sigDelimiterRe.MatchString(s) {
		return "signature delimiter"
	}
	return ""
}

// --- raw conversation transcripts ---

var (
	roleMarkerRe = regexp.MustCompile(`(?im)^[ \t]*(?:assistant|human|ai|system|claude)[ \t]*:`)
	timestampRe  = regexp.MustCompile(`(?m)^[ \t]*\[\d{1,2}:\d{2}(?::\d{2})?\][ \t]*\S`)
	speakerNRe   = regexp.MustCompile(`(?im)^[ \t]*speaker[ \t]*\d+[ \t]*:`)
	turnRe       = regexp.MustCompile(`(?m)^[ \t]*(?:\[\d{1,2}:\d{2}(?::\d{2})?\][ \t]*)?([A-Z][A-Za-z.'-]*(?: [A-Z][A-Za-z.'-]*){0,3}|User)[ \t]*:[ \t]+\S`)
)

func transcriptRule(s string) string {
	if roleMarkerRe.MatchString(s) {
		return "assistant/human role markers"
	}
	if len(speakerNRe.FindAllStringIndex(s, -1)) >= 2 {
		return "speaker turns"
	}
	if len(timestampRe.FindAllStringIndex(s, -1)) >= 2 {
		return "timestamped turns"
	}
	// Named turns: at least three "Name: ..." lines by two or more
	// speakers, one of them speaking twice. A real exchange goes back and
	// forth; two "Key: value" lines in a fact ("Budget: $40k" / "Owner:
	// Dana") do not.
	count := map[string]int{}
	turns := 0
	repeat := false
	for _, m := range turnRe.FindAllStringSubmatch(s, -1) {
		turns++
		count[m[1]]++
		if count[m[1]] >= 2 {
			repeat = true
		}
	}
	if turns >= 3 && len(count) >= 2 && repeat {
		return "multi-speaker turns"
	}
	return ""
}
