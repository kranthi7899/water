package approvals

import (
	"context"
	"errors"
	"testing"
)

// Review of Slice W: Propose must check every address a to/cc/bcc item
// carries, not just the one Address extracts; otherwise
// "x@therightgmail.com, K <kranthi@gmail.com>" was proposed clean (and so
// even voice-confirmable) while the mail would go to both.
func TestProposeRefusesHiddenSecondAddress(t *testing.T) {
	q, _ := newTestQueue(t)
	p := map[string]any{"to": []any{"kranthetjob@therightgmail.com, Kranthi <kranthi@gmail.com>"}, "subject": "Job", "body": "Hi"}
	if _, err := q.Propose(context.Background(), sendEnv(p)); !errors.Is(err, ErrRecipient) {
		t.Fatalf("Propose err = %v, want ErrRecipient", err)
	}
}
