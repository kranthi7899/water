package memory

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func cleanRecord(statement string) Record {
	return Record{
		Type: Preference, Statement: statement, Sensitivity: Normal,
		Provenance: Provenance{Trigger: CEOStatement, SourceRef: "audit:12", AuditSeq: 12, WrittenBy: AuthorCEO},
		Time:       TimeBounds{ObservedAt: time.Date(2026, 10, 3, 9, 0, 0, 0, time.UTC)},
		Confidence: 1,
	}
}

// rejectCorpus has at least one case per category, each with the text
// that must never appear in the resulting error message.
var rejectCorpus = []struct {
	name   string
	text   string
	cat    Category
	secret string // a fragment of text that must not be echoed
}{
	{"gmail body with headers", "From: Dana Ruiz <dana@acme.com>\nTo: ceo@water.dev\nSubject: Q4 plan\nDate: Mon, 22 Sep 2026 10:14:00 -0700\n\nHi, attached is the plan.", CatRawMessage, "dana@acme.com"},
	{"message-id header alone", "Message-ID: <CAF=abc123@mail.gmail.com>\nsee thread", CatRawMessage, "CAF=abc123"},
	{"quoted reply thread", "Sounds good, ship it.\n\nOn Mon, Sep 22, 2026 at 10:14 AM Dana Ruiz <dana@acme.com> wrote:\n> Can we move the offsite?\n> Thanks", CatRawMessage, "move the offsite"},
	{"quoted lines only", "> we should cut the budget\n> by 20 percent", CatRawMessage, "cut the budget"},
	{"forwarded marker", "---------- Forwarded message ---------\nplan attached", CatRawMessage, "plan attached"},
	{"signature delimiter", "Let's meet Thursday.\n-- \nDana Ruiz\nVP Engineering", CatRawMessage, "VP Engineering"},
	{"over the length cap", strings.Repeat("The CEO said a lot of things. ", 40), CatRawMessage, "said a lot"},
	{"two-speaker transcript", "Dana: I think we should delay the launch.\nKranthi: Why?\nDana: QA found two blockers.\nKranthi: OK, push it a week.", CatRawTranscript, "QA found two blockers"},
	{"speaker N transcript", "Speaker 1: the numbers look off\nSpeaker 2: which ones", CatRawTranscript, "numbers look off"},
	{"timestamped transcript", "[09:14] we start with hiring\n[09:15] then the budget", CatRawTranscript, "start with hiring"},
	{"assistant role marker", "User: remember my flight\nAssistant: Sure, I'll remember it.", CatRawTranscript, "remember my flight"},
	{"PEM key", "key is -----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAA\n-----END OPENSSH PRIVATE KEY-----", CatCredential, "b3BlbnNzaC1rZXktdjEAAAA"},
	{"ya29 token", "use ya29.a0AfH6SMBx3kq9ZtLrQpWnE7vYuT2mCd for calendar", CatCredential, "a0AfH6SMBx3kq9ZtLrQpWnE7vYuT2mCd"},
	{"google refresh token", "refresh 1//0gLx7Qm2VbN9pRtY5wKsZeUa3", CatCredential, "0gLx7Qm2VbN9pRtY5wKsZeUa3"},
	{"JWT", "session eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U", CatCredential, "dozjgNryP4J3jVmNHl0w5N"},
	{"bearer header", "Authorization: Bearer 8f3kQ9zLm2Xv7RtBn4Ws1YpE", CatCredential, "8f3kQ9zLm2Xv7RtBn4Ws1YpE"},
	{"sk- key", "the key sk-proj-Ab3dEf6hIj9kLm2nOp5qRs8t", CatCredential, "Ab3dEf6hIj9kLm2nOp5qRs8t"},
	{"github token", "ghp_1234567890abcdefghijABCDEFGHIJ123456", CatCredential, "1234567890abcdefghij"},
	{"slack token", "xoxb-123456789012-abcdefABCDEF", CatCredential, "abcdefABCDEF"},
	{"aws key id", "AKIAIOSFODNN7EXAMPLE is the id", CatCredential, "IOSFODNN7EXAMPLE"},
	{"password assignment", "wifi password: hunter2hunter2", CatCredential, "hunter2hunter2"},
	{"unrecognized high-entropy token", "api key Zq8XvR2mLp4TnW7kYs3JdF9bHc6GaE1uQo5VtNi0", CatCredential, "Zq8XvR2mLp4TnW7kYs3JdF9bHc6GaE1uQo5VtNi0"},
	{"luhn card spaced", "corporate card 4111 1111 1111 1111", CatPayment, "4111 1111"},
	{"luhn card contiguous", "card 5555555555554444 for AWS", CatPayment, "5555555555554444"},
	{"luhn amex", "amex 3782 822463 10005", CatPayment, "822463"},
	{"card-like with cvv", "card 4111 1111 1111 1112 cvv 123", CatPayment, "1112"},
	{"card-like with expiry", "card 4111111111111112 exp 09/28", CatPayment, "4111111111111112"},
	{"IBAN", "wire to DE89 3704 0044 0532 0130 00 monthly", CatPayment, "3704 0044"},
	{"IBAN contiguous", "GB82WEST12345698765432", CatPayment, "WEST12345698765432"},
	{"malformed vault handle", "pay with vault:4111111111111111", CatPayment, "4111111111111111"},
	{"vault handle with no account", "pay with vault:cards/", CatPayment, "cards/"},
	{"SSN dashed", "Dana's SSN is 123-45-6789", CatGovernmentID, "123-45-6789"},
	{"SSN bare", "tax form lists 123456789 for Dana", CatGovernmentID, "123456789"},
}

var acceptCorpus = []string{
	"The Q4 hiring budget is $1,250,000.",
	"Board updates go out on the first Monday of the month, starting 2026-10-05.",
	"Prefers morning meetings before 11:00.",
	"Dana leads the Crane project.",
	"Pay the AWS invoice with vault:cards/amex-corporate.",
	"Invoice 12345678901234567 is disputed.", // 17 digits, not Luhn-valid
	"Revenue was 4,111,111 last quarter.",
	"Commit 3f786850e387550fdab836ed7e6dc881de23001b fixed the sync bug.", // hex, no upper case
	"Standups:\n9:30 engineering\n10:00 product",
	"Budget: $40k\nOwner: Dana",
	"Subject lines should be short.",
	"Invalid-range numbers like 000-12-3456 and 900-12-3456 are not SSNs.",
	"Ticket ENG-1234 tracks the migration.",
	"The password policy is rotate quarterly.",
	"Call on 2026-10-05 2026-10-12 for the review.",
}

func TestNeverStoreRejectCorpus(t *testing.T) {
	for _, c := range rejectCorpus {
		err := CheckNeverStore(cleanRecord(c.text))
		var ns ErrNeverStore
		if !errors.As(err, &ns) {
			t.Errorf("%s: accepted, want %s", c.name, c.cat)
			continue
		}
		if ns.Category != c.cat || ns.Field != "statement" {
			t.Errorf("%s: got %s in %s, want %s in statement", c.name, ns.Category, ns.Field, c.cat)
		}
		if strings.Contains(err.Error(), c.secret) {
			t.Errorf("%s: error echoes the rejected text: %v", c.name, err)
		}
	}
}

func TestNeverStoreAcceptCorpus(t *testing.T) {
	for _, s := range acceptCorpus {
		r := cleanRecord(s)
		if err := CheckNeverStore(r); err != nil {
			t.Errorf("%q: %v", s, err)
		}
		if err := r.Validate(); err != nil {
			t.Errorf("%q: validate: %v", s, err)
		}
	}
	// Opaque provider ids in refs are high-entropy by design and pass.
	r := cleanRecord("The board deck is in Drive.")
	r.Provenance.SourceRef = "gdrive:1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms"
	r.Subjects = []string{"doc:1BxiMVs0XRA5nFMdKvBdBZjgmUUqptlbs74OgvE2upms", "person:dana"}
	if err := CheckNeverStore(r); err != nil {
		t.Errorf("drive id ref: %v", err)
	}
}

// TestNeverStoreInRefs checks every category in SourceRef and Subjects,
// not just Statement. A pasted body fails as never-store content before
// the ref's shape check, so the category is reported.
func TestNeverStoreInRefs(t *testing.T) {
	cases := []struct {
		ref string
		cat Category
	}{
		{"x:ya29.a0AfH6SMBx3kq9ZtLrQpWnE7vYuT2mCd", CatCredential},
		{"x:eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N", CatCredential},
		{"card:4111111111111111", CatPayment},
		{"iban:GB82WEST12345698765432", CatPayment},
		{"ssn:123-45-6789", CatGovernmentID},
		{"From: dana@acme.com\nSubject: plan\n\nbody", CatRawMessage},
		{"Dana: hi\nSam: hey\nDana: bye", CatRawTranscript},
	}
	for _, c := range cases {
		for _, where := range []string{"provenance.source_ref", "subjects[1]"} {
			r := cleanRecord("A clean statement.")
			if where == "provenance.source_ref" {
				r.Provenance.SourceRef = c.ref
			} else {
				r.Subjects = []string{"person:dana", c.ref}
			}
			var ns ErrNeverStore
			err := CheckNeverStore(r)
			if !errors.As(err, &ns) || ns.Category != c.cat || ns.Field != where {
				t.Errorf("%q in %s: got %v, want %s", c.ref, where, err, c.cat)
			}
		}
	}
	// Approver and invalidation strings are checked too.
	r := cleanRecord("A clean statement.")
	r.Provenance.Trigger, r.Provenance.WrittenBy, r.Provenance.ApprovedBy = ApprovedProposal, AuthorTwin, "sk-proj-Ab3dEf6hIj9kLm2nOp5qRs8t"
	if err := CheckNeverStore(r); err == nil {
		t.Error("credential in approved_by accepted")
	}
	r = cleanRecord("A clean statement.")
	r.Invalidation = &Invalidation{At: r.Time.ObservedAt, By: "ceo", Reason: "card 4111 1111 1111 1111", AuditSeq: 3}
	var ns ErrNeverStore
	if err := CheckNeverStore(r); !errors.As(err, &ns) || ns.Field != "invalidation.reason" {
		t.Errorf("payment in invalidation reason: %v", err)
	}
}

func TestLuhnAndIBAN(t *testing.T) {
	for d, want := range map[string]bool{"4111111111111111": true, "4111111111111112": false, "378282246310005": true, "79927398713": true} {
		if luhnValid(d) != want {
			t.Errorf("luhn(%s) != %v", d, want)
		}
	}
	for s, want := range map[string]bool{"DE89370400440532013000": true, "GB82WEST12345698765432": true, "GB83WEST12345698765432": false} {
		if ibanValid(s) != want {
			t.Errorf("iban(%s) != %v", s, want)
		}
	}
}
