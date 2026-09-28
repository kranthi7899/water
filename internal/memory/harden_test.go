package memory

import (
	"errors"
	"strings"
	"testing"
)

// TestNeverStoreEvasions are review regressions: forbidden content
// disguised with a letter glued to the digits, Unicode spaces, dashes or
// zero-width characters, full-width digits, or non-\n line breaks must
// still be refused, with the right category.
func TestNeverStoreEvasions(t *testing.T) {
	for _, c := range []struct {
		name, text string
		cat        Category
	}{
		{"card glued to letters", "corporate card visa4111111111111111 for travel", CatPayment},
		{"card glued to underscore", "corporate card_4111111111111111", CatPayment},
		{"grouped card glued to a letter", "card 4111 1111 1111 1111x", CatPayment},
		{"SSN glued to letters", "Dana ssn123-45-6789", CatGovernmentID},
		{"card with no-break spaces", "card 4111 1111 1111 1111", CatPayment},
		{"card with zero-width spaces", "card 4111​1111​1111​1111", CatPayment},
		{"card with non-breaking hyphens", "card 4111‑1111‑1111‑1111", CatPayment},
		{"card in full-width digits", "card ４１１１１１１１１１１１１１１１", CatPayment},
		{"SSN with no-break spaces", "SSN 123 45 6789", CatGovernmentID},
		{"sk- key split by a zero-width space", "key sk-​proj-Ab3dEf6hIj9kLm2nOp5qRs8t", CatCredential},
		{"headers split by bare CR", "From: dana@acme.com\rTo: ceo@water.dev\rSubject: plan", CatRawMessage},
		{"headers split by U+2028", "From: dana@acme.com To: ceo@water.dev", CatRawMessage},
		{"transcript split by bare CR", "Dana: delay the launch\rSam: why\rDana: QA blockers", CatRawTranscript},
		{"quoted reply split by U+2029", "ok > move the offsite > thanks", CatRawMessage},
	} {
		err := CheckNeverStore(cleanRecord(c.text))
		var ns ErrNeverStore
		if !errors.As(err, &ns) || ns.Category != c.cat {
			t.Errorf("%s: got %v, want %s", c.name, err, c.cat)
		}
	}
}

// TestValidRefRejectsUnicodeWhitespace: a ref is <scheme>:<id> with no
// whitespace. RE2's \S is ASCII-only, so no-break spaces, line separators,
// vertical tabs and zero-width characters used to pass as "not
// whitespace", letting a sentence ride in a ref.
func TestValidRefRejectsUnicodeWhitespace(t *testing.T) {
	for _, ref := range []string{
		"gmail:Dana said wire the money",
		"gmail:line two",
		"gmail:a\vb",
		"gmail:a​b",
		"gmail:a\x00b",
	} {
		r := cleanRecord("A clean statement.")
		r.Provenance.SourceRef = ref
		if err := r.Validate(); !errors.Is(err, ErrInvalidRecord) {
			t.Errorf("source_ref %q: %v, want ErrInvalidRecord", ref, err)
		}
		r = cleanRecord("A clean statement.")
		r.Subjects = []string{ref}
		if err := r.Validate(); !errors.Is(err, ErrInvalidRecord) {
			t.Errorf("subject %q: %v, want ErrInvalidRecord", ref, err)
		}
	}
	// Non-ASCII letters in an id are still fine.
	r := cleanRecord("A clean statement.")
	r.Subjects = []string{"person:josé"}
	if err := r.Validate(); err != nil {
		t.Errorf("unicode letter in subject: %v", err)
	}
}

// TestPrinciple6NoSelfApproval: the twin cannot be the approver of a
// record (or the author of an invalidation). "Approved by the twin" is no
// approval, and would let a twin-written record through principle 6.
func TestPrinciple6NoSelfApproval(t *testing.T) {
	for _, who := range []string{"twin", "Twin", " twin "} {
		r := cleanRecord("prefers async standups")
		r.Provenance.Trigger, r.Provenance.WrittenBy, r.Provenance.ApprovedBy = ApprovedProposal, AuthorTwin, who
		if err := r.Validate(); !errors.Is(err, ErrInvalidRecord) {
			t.Errorf("approved_by %q: %v, want ErrInvalidRecord", who, err)
		}
		inv := Invalidation{At: r.Time.ObservedAt, By: who, Reason: "no longer true", AuditSeq: 3}
		if err := inv.Validate(); !errors.Is(err, ErrInvalidRecord) {
			t.Errorf("invalidation by %q: %v, want ErrInvalidRecord", who, err)
		}
	}
	r := cleanRecord("prefers async standups")
	r.Provenance.Trigger, r.Provenance.WrittenBy, r.Provenance.ApprovedBy = ApprovedProposal, AuthorTwin, "ceo"
	if err := r.Validate(); err != nil {
		t.Fatalf("ceo-approved proposal: %v", err)
	}
}

// The accept corpus must still pass after normalization; this pins a few
// Unicode-heavy clean statements too.
func TestNeverStoreNormalizationKeepsCleanText(t *testing.T) {
	for _, s := range []string{
		"Dana leads the Crane project — launch 2026-10-05.",
		"Résumé review is on Thursday.",
		"Revenue grew 12 % last quarter.",
		strings.Repeat("a", 10) + "‍" + "b",
		// Hex identifiers with a 9-digit run between letters are not SSNs.
		"Commit a123456789bcdef0a123456789bcdef0a1234567 fixed it.",
	} {
		if err := CheckNeverStore(cleanRecord(s)); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	r := cleanRecord("Dana confirmed the date.")
	r.Provenance.SourceRef = "gmail:18c123456789abcd"
	r.Subjects = []string{"doc:ab123456789cdef01"}
	r.Supersedes = "mem_a123456789bcdef012345678"
	r.Invalidation = &Invalidation{At: r.Time.ObservedAt, By: "ceo", Reason: "moved", AuditSeq: 4, SupersededBy: "mem_0123456789abcdef01234567"}
	if err := CheckNeverStore(r); err != nil {
		t.Errorf("hex ids in refs and links: %v", err)
	}
	// Store-assigned ids must never trip the check, or Supersede would
	// fail at random.
	for i := 0; i < 5000; i++ {
		r := cleanRecord("Dana confirmed the date.")
		r.Supersedes = newID()
		r.Invalidation = &Invalidation{At: r.Time.ObservedAt, By: "ceo", Reason: "moved", AuditSeq: 4, SupersededBy: newID()}
		if err := CheckNeverStore(r); err != nil {
			t.Fatalf("random ids %s / %s: %v", r.Supersedes, r.Invalidation.SupersededBy, err)
		}
	}
}
