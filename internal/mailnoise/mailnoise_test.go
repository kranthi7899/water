package mailnoise

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type fixture struct {
	Subject string `json:"subject"`
	From    string `json:"from"`
	Snippet string `json:"snippet"`
}

func loadFixture(t *testing.T, name string) fixture {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	var f fixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatalf("parsing fixture %s: %v", name, err)
	}
	return f
}

// reasonsMention reports whether any reason (or the class) contains needle,
// case-insensitively. Reasons are always fixed, generic labels (never the
// message's own sender/domain/subject text), so this should always be
// false for a domain/company name — these tests prove it.
func reasonsMention(v Verdict, needle string) bool {
	needle = strings.ToLower(needle)
	if strings.Contains(strings.ToLower(v.Class), needle) {
		return true
	}
	for _, r := range v.Reasons {
		if strings.Contains(strings.ToLower(r), needle) {
			return true
		}
	}
	return false
}

var fixtureNames = []string{"is_this_your_paper.json", "claim_your_account.json"}

func TestFixturesClassifyAsNoise(t *testing.T) {
	for _, name := range fixtureNames {
		f := loadFixture(t, name)
		v := Classify(f.From, f.Subject, f.Snippet, Signals{}, nil)
		if v.Class != ClassNoise {
			t.Errorf("%s: Class = %q, want %q (reasons: %v)", name, v.Class, ClassNoise, v.Reasons)
		}
		if reasonsMention(v, "academia") || reasonsMention(v, "scholar") || reasonsMention(v, "members") {
			t.Errorf("%s: reasons mention a hardcoded domain/company name: %v", name, v.Reasons)
		}
	}
}

// TestFixturesStayNoiseWithGenericDomain rewrites each fixture's sender
// domain to a made-up domain and re-classifies: still noise, proving the
// verdict comes from general signals (bulk shape, claim pattern), never a
// hardcoded domain.
func TestFixturesStayNoiseWithGenericDomain(t *testing.T) {
	for _, name := range fixtureNames {
		f := loadFixture(t, name)
		local, _ := splitAddress(f.From)
		rewritten := local + "@papers-mail.example"
		v := Classify(rewritten, f.Subject, f.Snippet, Signals{}, nil)
		if v.Class != ClassNoise {
			t.Errorf("%s with rewritten domain %q: Class = %q, want %q", name, rewritten, v.Class, ClassNoise)
		}
	}
}

// TestFixturesNoiseViaHeaderRuleAlone proves the header/label rule fires by
// itself: a message with none of the content signals (no claim phrase, no
// bulk-shaped sender) is still noise once a bulk-mail header or label is
// present.
func TestFixturesNoiseViaHeaderRuleAlone(t *testing.T) {
	cases := []struct {
		name string
		sig  Signals
	}{
		{"list-unsubscribe", Signals{ListUnsubscribe: "<mailto:unsub@example.com>"}},
		{"list-id", Signals{ListID: "<updates.example.com>"}},
		{"precedence-bulk", Signals{Precedence: "bulk"}},
		{"precedence-list", Signals{Precedence: "list"}},
		{"precedence-junk", Signals{Precedence: "junk"}},
		{"auto-submitted", Signals{AutoSubmitted: "auto-generated"}},
		{"label-promotions", Signals{Labels: []string{"CATEGORY_PROMOTIONS"}}},
		{"label-updates", Signals{Labels: []string{"CATEGORY_UPDATES"}}},
		{"label-social", Signals{Labels: []string{"CATEGORY_SOCIAL"}}},
		{"label-forums", Signals{Labels: []string{"CATEGORY_FORUMS"}}},
	}
	from := "Dana Lee <dana@ordinary-example.com>"
	subject := "Quarterly report attached"
	snippet := "Here is the quarterly report you asked for. Let me know if anything looks off."
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := Classify(from, subject, snippet, c.sig, nil)
			if v.Class != ClassNoise {
				t.Errorf("Class = %q, want %q (reasons: %v)", v.Class, ClassNoise, v.Reasons)
			}
		})
	}
}

// TestAutoSubmittedNoDoesNotFire checks the one documented exception:
// Auto-Submitted: no is not a noise signal (RFC 3834's "this is a real
// human reply" value).
func TestAutoSubmittedNoDoesNotFire(t *testing.T) {
	from := "Dana Lee <dana@ordinary-example.com>"
	v := Classify(from, "Re: quarterly report", "Looks good, thanks!", Signals{AutoSubmitted: "no"}, nil)
	if v.Class == ClassNoise {
		t.Errorf("Auto-Submitted: no should not, by itself, mark noise; got reasons %v", v.Reasons)
	}
}

// TestControlPlainHumanQuestionIsNotNoise is the brief's own negative
// control: an ordinary short question from a colleague at a normal domain.
func TestControlPlainHumanQuestionIsNotNoise(t *testing.T) {
	v := Classify("Dana Lee <dana@ordinary-example.com>", "Quick question", "Can we meet at 3pm today?", Signals{}, nil)
	if v.Class == ClassNoise {
		t.Errorf("plain human question should not be noise; got Class=%q reasons=%v", v.Class, v.Reasons)
	}
}

// TestControlClaimFromKnownDomainIsNotNoise is the brief's other negative
// control: a claim-pattern message from a domain the CEO has actually
// written to before is not noise, even though it has the same bulk-shaped
// sender and claim phrasing as a noise fixture.
func TestControlClaimFromKnownDomainIsNotNoise(t *testing.T) {
	wroteTo := func(domain string) bool { return domain == "mail.knownvendor.example" }
	v := Classify("billing@mail.knownvendor.example", "Is this your account?", "Please confirm your details on file.", Signals{}, wroteTo)
	if v.Class == ClassNoise {
		t.Errorf("a claim-pattern message from a domain the CEO wrote to should not be noise; got reasons %v", v.Reasons)
	}
}

// TestBulkSenderShapeWithoutClaimIsInformative covers rule 3: a bulk-shaped
// sender with no claim/confirm phrase is informative, not noise.
func TestBulkSenderShapeWithoutClaimIsInformative(t *testing.T) {
	v := Classify("updates@ordinary-example.com", "Your weekly digest", "Here is what happened this week.", Signals{}, nil)
	if v.Class != ClassInformative {
		t.Errorf("Class = %q, want %q (reasons: %v)", v.Class, ClassInformative, v.Reasons)
	}
}

// TestDomainSegmentMatchIsWholeSegmentOnly proves the domain-shape signal
// only matches a whole '.'/'-' separated segment, never a substring that
// merely contains one of the marker words.
func TestDomainSegmentMatchIsWholeSegmentOnly(t *testing.T) {
	cases := []struct {
		domain   string
		wantBulk bool
	}{
		{"mail.example.com", true},
		{"x-mail.com", true},
		{"example.com", false},
		{"germaine.com", false}, // contains "email"-adjacent letters, not a whole segment
		{"remail.com", false},   // "remail" is not the whole segment "mail"
	}
	for _, c := range cases {
		got := senderLooksBulk("person@"+c.domain, c.domain)
		if got != c.wantBulk {
			t.Errorf("senderLooksBulk(domain=%q) = %v, want %v", c.domain, got, c.wantBulk)
		}
	}
}

// TestPreheaderPaddingSignal proves the >=5 consecutive invisible/combining
// character run is required, not merely present.
func TestPreheaderPaddingSignal(t *testing.T) {
	pad5 := strings.Repeat("​", 5)
	pad4 := strings.Repeat("​", 4)
	from := "person@ordinary-example.com" // not bulk-shaped, so padding is the only content signal
	subject := "Did you write this review?"
	if v := Classify(from, subject, "Great product!"+pad5+"Buy now", Signals{}, nil); v.Class != ClassNoise {
		t.Errorf("5-run padding + claim phrase: Class = %q, want %q (reasons %v)", v.Class, ClassNoise, v.Reasons)
	}
	if v := Classify(from, subject, "Great product!"+pad4+"Buy now", Signals{}, nil); v.Class == ClassNoise {
		t.Errorf("a 4-run of padding characters should not by itself trigger noise; got reasons %v", v.Reasons)
	}
}

// TestNilWroteToDefaultsFalse proves a nil wroteTo behaves like a wroteTo
// that always reports false, rather than panicking.
func TestNilWroteToDefaultsFalse(t *testing.T) {
	v := Classify("updates@mail.example.com", "Is this your paper?", "Claim your profile now.", Signals{}, nil)
	if v.Class != ClassNoise {
		t.Errorf("Class = %q, want %q", v.Class, ClassNoise)
	}
}
