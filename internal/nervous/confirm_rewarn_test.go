package nervous

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"water/internal/approvals"
)

// errResolver fails every lookup, so a checker built on it can only ever
// report "couldn't verify" for a non-provider domain.
type errResolver struct{}

func (errResolver) LookupMX(context.Context, string) ([]*net.MX, error) {
	return nil, errors.New("dns unavailable")
}
func (errResolver) LookupHost(context.Context, string) ([]string, error) {
	return nil, errors.New("dns unavailable")
}

// Integration seam (Slice W, S3 x S4): warnings are recomputed on every
// queue read, so an envelope armed at stage one with no warnings can carry
// one by stage two (an expired MX cache entry, a restart). D5b says an
// envelope with warnings is tap-only, so "confirm send" must re-check the
// tier and refuse to execute.
func TestVoiceConfirmSendRechecksWarningsAtStageTwo(t *testing.T) {
	h := newVoiceBindHarness(t, "high", nil, true)
	p := sendPayload()
	p["to"] = []any{"ops@partner-example.io"}
	env := proposeSend(t, h, p)

	h.advance(2 * time.Second)
	events := h.turn(t, "yes", "stage1")
	if confirmPhraseOf(events) != "confirm send" {
		t.Fatalf("precondition: stage one must arm with no warnings (events=%v)", events)
	}

	// The domain can no longer be verified: the read path now warns.
	h.env.Approvals.SetRecipientChecker(approvals.NewRecipientChecker(errResolver{}, nil))
	pend, _ := h.env.Approvals.Pending(h.ctx)
	if len(pend) != 1 || len(pend[0].Warnings) == 0 {
		t.Fatalf("precondition: envelope must now carry a warning, got %+v", pend)
	}

	h.advance(3 * time.Second)
	events = h.turn(t, "confirm send", "stage2")
	if h.approver.callCount() != 0 {
		t.Fatalf("DecideBound calls = %d, want 0: a warned envelope is tap-only", h.approver.callCount())
	}
	if statusOf(t, h, env.ID) != approvals.Pending {
		t.Fatal("envelope must stay pending for a tap")
	}
	if !hasApprovalRequired(events) || confirmPhraseOf(events) != "" || !strings.Contains(spokenText(events), "tap") {
		t.Fatalf("stage two events = %v, want tap_required with no confirm phrase", events)
	}
}
