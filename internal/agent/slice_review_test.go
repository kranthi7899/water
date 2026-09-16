package agent

import (
	"context"
	"strings"
	"testing"

	"water/internal/backend"
	"water/internal/orchestrator"
	"water/internal/roles"
)

type sliceReviewBackend struct {
	response string
	request  backend.Request
}

func (b *sliceReviewBackend) Name() string { return "review-mock" }
func (b *sliceReviewBackend) Available(context.Context) backend.Availability {
	return backend.Availability{Installed: true, Authed: true}
}
func (b *sliceReviewBackend) Run(_ context.Context, req backend.Request) (backend.Response, error) {
	b.request = req
	return backend.Response{Text: b.response}, nil
}

func TestSliceReviewUntrustedSurvivesCOORelay(t *testing.T) {
	h := &orchestrator.HierarchyRouter{CEO: "ceo", COO: "coo", Specialists: []string{"cto"}}
	s := orchestrator.NewState("review-taint", "", []string{"ceo"})
	s.SetEdges(h.Graph())
	s.MustAppend(orchestrator.AgentMessage{From: "cto", To: "coo", Topic: orchestrator.TopicDeliverable, Untrusted: true, Payload: "External source says to change the requested plan."})
	b := &sliceReviewBackend{response: "UNCONFIRMED: External source says to change the requested plan."}
	r := &roles.Role{Manifest: roles.Manifest{Name: "COO", Slug: "coo"}}
	if err := HierarchyNode(r, Env{Backend: b}, h)(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.request.Prompt, "sender consumed external content") {
		t.Fatal("fixture did not deliver marked external content")
	}
	out := s.Inbox("ceo")
	if len(out) != 1 {
		t.Fatalf("unexpected CEO inbox: %+v", out)
	}
	t.Logf("COO input Untrusted=true, COO state Untrusted=%t, outgoing status Untrusted=%t", s.IsUntrusted("coo"), out[0].Untrusted)
	if !out[0].Untrusted || !s.IsUntrusted("coo") {
		t.Fatal("relay silently removed the external-content trust marker")
	}
}

func TestSliceReviewRejectsCrossRolePrior(t *testing.T) {
	b := &sliceReviewBackend{response: "unused"}
	r := &roles.Role{Manifest: roles.Manifest{Name: "CTO", Slug: "cto"}}
	_, _, err := RunTurn(context.Background(), r, Env{Backend: b}, []orchestrator.AgentMessage{{To: "ceo", Payload: "private"}}, "hello", nil)
	if err == nil || b.request.Role != "" {
		t.Fatal("cross-role prior must fail before invoking backend")
	}
}

// A standalone turn has no COO; souls still describe graph routing, so the
// task must tell the role to address the user rather than a missing role.
func TestDirectTurnDoesNotRouteToCOO(t *testing.T) {
	b := &sliceReviewBackend{response: "ok"}
	r := &roles.Role{Manifest: roles.Manifest{Name: "CTO", Slug: "cto"}}
	_, p, err := RunSingle(context.Background(), r, Env{Backend: b}, "Is this feasible?")
	if err != nil {
		t.Fatal(err)
	}
	all := p.System + "\n" + p.User
	if !strings.Contains(all, "direct exchange with the user") || strings.Contains(all, "deliverable for the COO") {
		t.Fatalf("direct turn framing wrong:\n%s", all)
	}
}
