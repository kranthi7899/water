package decisions

import (
	"errors"
	"testing"
)

// TestValidEvidenceKindClosedSet checks Evidence.Kind's closed enum
// (docs/slices/UI.md Phase 1c): "" and the six named icons are valid,
// anything else is not.
func TestValidEvidenceKindClosedSet(t *testing.T) {
	valid := []string{"", EvidenceKindMoney, EvidenceKindCustomer, EvidenceKindIssue, EvidenceKindMail, EvidenceKindCalendar, EvidenceKindResearch}
	for _, k := range valid {
		if !ValidEvidenceKind(k) {
			t.Errorf("ValidEvidenceKind(%q) = false, want true", k)
		}
	}
	invalid := []string{"MONEY", "money ", "attachment", "priority", "free text kind"}
	for _, k := range invalid {
		if ValidEvidenceKind(k) {
			t.Errorf("ValidEvidenceKind(%q) = true, want false", k)
		}
	}
}

// TestCardValidateRejectsInvalidEvidenceKind: wherever Evidence.Kind gets
// set to something outside the closed set, Card.Validate must catch it,
// the same "report every violation at once" pattern it already uses for
// unsourced evidence.
func TestCardValidateRejectsInvalidEvidenceKind(t *testing.T) {
	c := &Card{Evidence: []Evidence{
		{Text: "ok", Source: "code:x", Kind: EvidenceKindMoney},
		{Text: "bad", Source: "code:y", Kind: "not-a-real-kind"},
	}}
	err := c.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want an error for the invalid evidence kind")
	}
	if !errors.Is(err, ErrInvalidEvidenceKind) {
		t.Fatalf("Validate() = %v, want it to wrap ErrInvalidEvidenceKind", err)
	}
}

// TestQuarantineClearsInvalidEvidenceKindRatherThanDroppingTheEvidence: an
// evidence line with a bad Kind but a good Source is not unsourced, so
// quarantine keeps the evidence and only strips the bad icon, noting it as
// a gap -- consistent with quarantine's existing "state the removal, never
// hide it" behavior for unsourced figures.
func TestQuarantineClearsInvalidEvidenceKindRatherThanDroppingTheEvidence(t *testing.T) {
	c := &Card{Readiness: Ready, Evidence: []Evidence{{Text: "keep me", Source: "code:x", Kind: "bogus"}}}
	if c.Validate() == nil {
		t.Fatal("expected Validate to reject the bogus kind before quarantine")
	}
	c.quarantine()
	if len(c.Evidence) != 1 {
		t.Fatalf("Evidence = %+v, want the item kept (it has a source, only the kind was bad)", c.Evidence)
	}
	if c.Evidence[0].Kind != "" {
		t.Fatalf("Kind = %q, want quarantine to clear it to \"\"", c.Evidence[0].Kind)
	}
	if len(c.Gaps) == 0 {
		t.Fatal("quarantine must state the removal as a gap, never hide it")
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("after quarantine, Validate() = %v, want nil", err)
	}
}
