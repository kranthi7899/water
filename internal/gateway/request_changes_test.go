package gateway

import (
	"context"
	"net/http"
	"testing"

	"water/internal/approvals"
	"water/internal/store"
)

// personRequest proposes a pending person-request approval RequestedBy id,
// the shape a real "Requested by <name>" card has.
func personRequest(t *testing.T, h *harness, requestedBy string) approvals.Envelope {
	t.Helper()
	env, err := h.q.ProposeRequest(context.Background(), approvals.Envelope{
		RequestedBy: requestedBy, Origin: "p0", Payload: map[string]any{"request_id": "r1", "answer": "pending"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// TestRequestChangesDeniesAndProposesAReply is request-changes' success
// path: the requester resolves to a roster address, the original is denied
// with the fixed reason, and exactly one new gmail.send_message envelope is
// proposed to that address -- never executed.
func TestRequestChangesDeniesAndProposesAReply(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.st.Upsert(ctx, &store.Person{
		Meta: store.Meta{Source: "seed", SourceID: "lee"}, Name: "Lee Chen",
		Identities: `{"email":"lee@partner.example"}`,
	}); err != nil {
		t.Fatal(err)
	}
	env := personRequest(t, h, "lee")

	var out requestChangesResponse
	decodeInto(t, do(t, h.srv.URL, "POST", "/v1/approvals/"+env.ID+"/request-changes", `{"note":"Can you clarify the amount?"}`, h.token),
		http.StatusOK, &out)

	if out.Denied.ID != env.ID || out.Denied.Status != approvals.Denied || out.Denied.Reason != "changes requested" {
		t.Fatalf("denied = %+v", out.Denied)
	}
	if out.Envelope.Status != approvals.Pending || out.Envelope.Action != "gmail.send_message" {
		t.Fatalf("new envelope = %+v", out.Envelope)
	}
	to, _ := out.Envelope.Payload["to"].([]any)
	if len(to) != 1 || to[0] != "lee@partner.example" {
		t.Fatalf("to = %v, want [lee@partner.example]", out.Envelope.Payload["to"])
	}
	if out.Envelope.Payload["body"] != "Can you clarify the amount?" {
		t.Fatalf("body = %v", out.Envelope.Payload["body"])
	}

	// Exactly one new envelope: the original plus one reply, nothing more.
	pending, err := h.q.Pending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != out.Envelope.ID {
		t.Fatalf("pending = %+v, want exactly the one new reply", pending)
	}
}

// TestRequestChangesFailsClosedBeforeMutatingAnything covers every
// unresolvable-requester case: the original is left untouched (still
// pending) and no new envelope is proposed, and the failure is 409 in each
// case (check-then-mutate, never the reverse).
func TestRequestChangesFailsClosedBeforeMutatingAnything(t *testing.T) {
	cases := []struct {
		name        string
		requestedBy string
		seedPerson  *store.Person
	}{
		{name: "not a person request at all (no RequestedBy)", requestedBy: ""},
		{name: "RequestedBy names nobody in the roster", requestedBy: "ghost"},
		{
			name: "the person has no email on file", requestedBy: "lee",
			seedPerson: &store.Person{Meta: store.Meta{Source: "seed", SourceID: "lee"}, Name: "Lee Chen"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t)
			ctx := context.Background()
			if c.seedPerson != nil {
				if err := h.st.Upsert(ctx, c.seedPerson); err != nil {
					t.Fatal(err)
				}
			}
			var env approvals.Envelope
			if c.requestedBy == "" {
				var err error
				env, err = h.q.Propose(ctx, approvals.Envelope{Action: "gmail.send_message", Origin: "p0", Payload: map[string]any{"to": []string{"x@example.com"}, "subject": "s", "body": "b"}})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				env = personRequest(t, h, c.requestedBy)
			}

			if got := statusOf(do(t, h.srv.URL, "POST", "/v1/approvals/"+env.ID+"/request-changes", `{"note":"please clarify"}`, h.token)); got != http.StatusConflict {
				t.Fatalf("status = %d, want 409", got)
			}
			cur, err := h.q.Get(ctx, env.ID)
			if err != nil {
				t.Fatal(err)
			}
			if cur.Status != approvals.Pending {
				t.Fatalf("the original was mutated: status = %s, want still pending", cur.Status)
			}
			pending, err := h.q.Pending(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if len(pending) != 1 {
				t.Fatalf("pending = %+v, want only the untouched original (no reply proposed)", pending)
			}
		})
	}
}

// TestRequestChangesValidation covers the endpoint's simpler refusals: an
// empty note, an unknown approval id, and an approval that is no longer
// pending.
func TestRequestChangesValidation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.st.Upsert(ctx, &store.Person{
		Meta: store.Meta{Source: "seed", SourceID: "lee"}, Name: "Lee Chen", Identities: `{"email":"lee@partner.example"}`,
	}); err != nil {
		t.Fatal(err)
	}

	empty := personRequest(t, h, "lee")
	if got := statusOf(do(t, h.srv.URL, "POST", "/v1/approvals/"+empty.ID+"/request-changes", `{"note":"  "}`, h.token)); got != http.StatusBadRequest {
		t.Fatalf("empty note: status %d, want 400", got)
	}

	if got := statusOf(do(t, h.srv.URL, "POST", "/v1/approvals/env_nope/request-changes", `{"note":"x"}`, h.token)); got != http.StatusNotFound {
		t.Fatalf("unknown id: status %d, want 404", got)
	}

	decided := personRequest(t, h, "lee")
	if _, err := h.q.Decide(ctx, decided.ID, approvals.No); err != nil {
		t.Fatal(err)
	}
	if got := statusOf(do(t, h.srv.URL, "POST", "/v1/approvals/"+decided.ID+"/request-changes", `{"note":"x"}`, h.token)); got != http.StatusConflict {
		t.Fatalf("already-decided: status %d, want 409", got)
	}
}
