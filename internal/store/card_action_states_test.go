package store

import (
	"context"
	"errors"
	"testing"
)

// TestTwoActionsOnOneCardStageIndependently is the concrete behavior
// card_action_states exists for (docs/slices/UI.md Phase 1c, finding 20):
// the old card_states design held one ApprovalID per CardID, so staging a
// second action while the first was pending simply overwrote it -- there
// was no way to tell "action A is staged into env_1" apart from "action B
// is staged into env_2" on the same card. Keying by (CardID, ActionID)
// fixes that: setting one action's state must never touch the other's.
func TestTwoActionsOnOneCardStageIndependently(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	if err := s.SetCardActionState(ctx, CardActionState{CardID: "card-1", ActionID: "gmail.send_message", Status: "staged", ApprovalID: "env_email"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCardActionState(ctx, CardActionState{CardID: "card-1", ActionID: "linear.set_issue_priority", Status: "staged", ApprovalID: "env_priority"}); err != nil {
		t.Fatal(err)
	}

	email, err := s.GetCardActionState(ctx, "card-1", "gmail.send_message")
	if err != nil {
		t.Fatal(err)
	}
	if email.ApprovalID != "env_email" {
		t.Fatalf("email action's approval = %q, want env_email (want it undisturbed by staging the priority action)", email.ApprovalID)
	}
	priority, err := s.GetCardActionState(ctx, "card-1", "linear.set_issue_priority")
	if err != nil {
		t.Fatal(err)
	}
	if priority.ApprovalID != "env_priority" {
		t.Fatalf("priority action's approval = %q, want env_priority", priority.ApprovalID)
	}

	all, err := s.CardActionStates(ctx, "card-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("CardActionStates = %+v, want both actions present", all)
	}

	// Re-staging one action (e.g. after an approval edit re-points it) must
	// only ever touch that action's own row.
	if err := s.SetCardActionState(ctx, CardActionState{CardID: "card-1", ActionID: "gmail.send_message", Status: "staged", ApprovalID: "env_email_v2"}); err != nil {
		t.Fatal(err)
	}
	email2, err := s.GetCardActionState(ctx, "card-1", "gmail.send_message")
	if err != nil || email2.ApprovalID != "env_email_v2" {
		t.Fatalf("email action after re-stage = %+v, %v", email2, err)
	}
	priority2, err := s.GetCardActionState(ctx, "card-1", "linear.set_issue_priority")
	if err != nil || priority2.ApprovalID != "env_priority" {
		t.Fatalf("priority action must be untouched by re-staging email: %+v, %v", priority2, err)
	}
}

func TestGetCardActionStateUnsetReturnsErrNotFound(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if _, err := s.GetCardActionState(ctx, "card-x", "gmail.send_message"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetCardActionState(unset) = %v, want ErrNotFound", err)
	}
}

// TestCardActionStatesIsolatedFromCardStates is the dismiss-vs-staging
// regression the split was meant to guarantee: staging an action in the new
// table must never write anything to card_states (dismiss's home), and
// dismissing a card in card_states must never write anything to
// card_action_states.
func TestCardActionStatesIsolatedFromCardStates(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	if err := s.SetCardActionState(ctx, CardActionState{CardID: "card-1", ActionID: "gmail.send_message", Status: "staged", ApprovalID: "env_1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCardState(ctx, "card-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("staging an action wrote into card_states: %v", err)
	}

	if err := s.SetCardState(ctx, CardState{CardID: "card-2", Status: "dismissed", Reason: "not relevant"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetCardActionState(ctx, "card-2", "gmail.send_message"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("dismissing a card wrote into card_action_states: %v", err)
	}
}
