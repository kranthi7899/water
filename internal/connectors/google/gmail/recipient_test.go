package gmail

import (
	"context"
	"errors"
	"strings"
	"testing"

	"water/internal/spokenemail"
)

// The 2026-09-26 incident: "at the rate" misheard as "at the right" became
// kranthetjob@therightgmail.com. Every gmail write now refuses that address
// before any request is made, with a message the model can act on.
func TestWritesRefuseIncidentNearMissBeforeAnyRequest(t *testing.T) {
	ts := newTokenServer(t)
	api := &gmailWriteAPI{resp: `{"id":"d1","message":{"id":"m1","threadId":"t1"}}`}
	srv := api.server()
	defer srv.Close()
	cl := newDirectClient(t, ts, srv)
	g := New(testAgentAddress).SetSignature(testSignature)
	args := func() map[string]any {
		return map[string]any{"to": []any{"kranthetjob@therightgmail.com"}, "subject": "Job", "body": "Hi"}
	}
	calls := map[string]func(context.Context, map[string]any) error{
		"draft_for_review": func(ctx context.Context, a map[string]any) error { _, err := g.draftForReview(ctx, cl, a); return err },
		"draft_message":    func(ctx context.Context, a map[string]any) error { _, err := g.draftMessage(ctx, cl, a); return err },
		"send_message":     func(ctx context.Context, a map[string]any) error { _, err := g.sendMessage(ctx, cl, a); return err },
	}
	for name, call := range calls {
		err := call(context.Background(), args())
		if !errors.Is(err, spokenemail.ErrNearMiss) {
			t.Fatalf("%s: err = %v, want a near-miss refusal", name, err)
		}
		if !strings.Contains(err.Error(), "gmail.com") || !strings.Contains(err.Error(), "confirm_unusual_recipient") {
			t.Fatalf("%s: refusal %q is not model-actionable", name, err)
		}
		// Bad syntax is refused even with the override.
		bad := args()
		bad["to"] = []any{"kranthi at gmail"}
		bad["confirm_unusual_recipient"] = true
		if err := call(context.Background(), bad); !errors.Is(err, spokenemail.ErrInvalid) {
			t.Fatalf("%s bad syntax: err = %v, want ErrInvalid", name, err)
		}
	}
	if api.calls() != 0 {
		t.Fatalf("API calls = %d, want 0: a refused recipient must never reach Gmail", api.calls())
	}

	// With the CEO's confirmation the draft goes ahead, and the confirm key
	// never reaches the built message.
	a := args()
	a["confirm_unusual_recipient"] = true
	if _, err := g.draftForReview(context.Background(), cl, a); err != nil {
		t.Fatalf("draft_for_review with confirmation: %v", err)
	}
	if api.calls() != 1 {
		t.Fatalf("calls = %d, want 1", api.calls())
	}
	msg := string(rawFromBody(t, api.last().body, true))
	if strings.Contains(msg, "confirm_unusual_recipient") || !strings.Contains(msg, "To: kranthetjob@therightgmail.com") {
		t.Fatalf("built message:\n%s", msg)
	}
}

func TestWriteSchemaAcceptsConfirmUnusualRecipient(t *testing.T) {
	for _, fn := range New(testAgentAddress).Functions() {
		switch fn.Name {
		case "draft_message", "send_message", "draft_for_review":
			ok := map[string]any{"to": []any{"a@b.io"}, "subject": "s", "body": "b", "confirm_unusual_recipient": true}
			if err := fn.Schema.Validate(ok); err != nil {
				t.Errorf("%s: %v", fn.Name, err)
			}
			ok["confirm_unusual_recipient"] = "yes"
			if fn.Schema.Validate(ok) == nil {
				t.Errorf("%s: a non-boolean confirm must be rejected", fn.Name)
			}
		}
	}
}
