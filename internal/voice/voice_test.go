package voice

import (
	"testing"
)

type explainer struct{ Noop }

func (explainer) Absence() string { return "custom reason" }

// TestAbsenceUsesProviderExplanation: any provider that explains itself is
// asked, not just the two concrete types voice knows about.
func TestAbsenceUsesProviderExplanation(t *testing.T) {
	if got := Absence(explainer{}); got != "custom reason" {
		t.Fatalf("Absence = %q, want the provider's own explanation", got)
	}
	if got := Absence(Noop{}); got != ErrUnavailable.Error() {
		t.Fatalf("noop Absence = %q", got)
	}
}
