package gateway

// TestSessionToolTokenIsStableAcrossTurns proves the design note in
// TwinToolPolicy: the token a turn's tool policy carries must not rotate,
// because the warm session's MCP bridge child reads its policy file once and
// serves many turns with it.
import (
	"testing"

	"water/internal/gate"
)

func TestSessionToolTokenIsStableAcrossTurns(t *testing.T) {
	h := newHarness(t)
	p1 := h.d.TwinToolPolicy()
	p2 := h.d.TwinToolPolicy()
	if p1.TwinToken == "" || p1.TwinToken != p2.TwinToken {
		t.Fatalf("tokens differ across calls: %q vs %q", p1.TwinToken, p2.TwinToken)
	}
}

func TestEscalateTaintIsStickyAndNeverResets(t *testing.T) {
	h := newHarness(t)
	tok := h.d.stableSessionToken()

	ta, ok := h.d.lookupTurnToken(tok)
	if !ok || ta.Taint != gate.Clean {
		t.Fatalf("expected a fresh session token to start clean, got %+v ok=%v", ta, ok)
	}

	h.d.escalateTaint(false) // a clean turn must not change anything
	ta, _ = h.d.lookupTurnToken(tok)
	if ta.Taint != gate.Clean {
		t.Fatalf("a clean turn escalated taint: %+v", ta)
	}

	h.d.escalateTaint(true) // one tainted turn ...
	ta, _ = h.d.lookupTurnToken(tok)
	if ta.Taint != gate.Tainted {
		t.Fatalf("a tainted turn did not escalate: %+v", ta)
	}

	h.d.escalateTaint(false) // ... and a later clean turn must not undo it
	ta, _ = h.d.lookupTurnToken(tok)
	if ta.Taint != gate.Tainted {
		t.Fatal("a later clean turn reset the session's taint; escalation must be sticky")
	}
}
