package meetings

import (
	"context"
	"errors"
	"testing"

	"water/internal/backend"
	"water/internal/store"
)

func testProjects() []ProjectOption {
	return []ProjectOption{{ID: "halcyon-rollout", Name: "Halcyon rollout"}, {ID: "meridian-renewal", Name: "Meridian renewal"}}
}

func classifyItem(text string) store.Record {
	return &store.Message{Meta: store.Meta{Source: "meetings", SourceID: "s1"}, Body: text}
}

// TestProjectClassifierPicksAKnownProjectAboveFloor: a confident, known
// project id is trusted as-is.
func TestProjectClassifierPicksAKnownProjectAboveFloor(t *testing.T) {
	fb := backend.NewFake("fake")
	fb.Reply = func(backend.Request) string { return `{"project_id":"halcyon-rollout","confidence":0.82}` }
	var charged int
	pc := &ProjectClassifier{Projects: testProjects(), Backend: fb, Model: "haiku", Charge: func() error { charged++; return nil }}
	c, err := pc.Classify(context.Background(), classifyItem("let's talk about the halcyon launch date"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Fallback || c.TypeID != "halcyon-rollout" || c.Confidence != 0.82 {
		t.Fatalf("classification = %+v", c)
	}
	if charged != 1 {
		t.Fatalf("charged = %d, want exactly 1 (one model call, one charge)", charged)
	}
	if fb.Calls() != 1 {
		t.Fatalf("backend calls = %d, want 1", fb.Calls())
	}
}

// TestProjectClassifierFallsBackBelowFloor: a low-confidence reply is
// reported as no guess, not a forced pick.
func TestProjectClassifierFallsBackBelowFloor(t *testing.T) {
	fb := backend.NewFake("fake")
	fb.Reply = func(backend.Request) string { return `{"project_id":"halcyon-rollout","confidence":0.2}` }
	pc := &ProjectClassifier{Projects: testProjects(), Backend: fb, Model: "haiku"}
	c, err := pc.Classify(context.Background(), classifyItem("vague chatter"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Fallback {
		t.Fatalf("classification = %+v, want Fallback (confidence under the floor)", c)
	}
}

// TestProjectClassifierFallsBackOnUnknownID: a project id outside the
// closed candidate list is never trusted, confidence notwithstanding.
func TestProjectClassifierFallsBackOnUnknownID(t *testing.T) {
	fb := backend.NewFake("fake")
	fb.Reply = func(backend.Request) string { return `{"project_id":"not-a-real-project","confidence":0.95}` }
	pc := &ProjectClassifier{Projects: testProjects(), Backend: fb, Model: "haiku"}
	c, err := pc.Classify(context.Background(), classifyItem("something"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Fallback {
		t.Fatalf("classification = %+v, want Fallback (id not in the candidate list)", c)
	}
}

// TestProjectClassifierUnreadableReplyIsFallbackNotError: a model reply with
// no JSON object is a Fallback verdict, matching decisions.ModelClassifier's
// own posture (an unreadable reply is not a failed call).
func TestProjectClassifierUnreadableReplyIsFallbackNotError(t *testing.T) {
	fb := backend.NewFake("fake")
	fb.Reply = func(backend.Request) string { return "sorry, I don't know" }
	pc := &ProjectClassifier{Projects: testProjects(), Backend: fb, Model: "haiku"}
	c, err := pc.Classify(context.Background(), classifyItem("something"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Fallback {
		t.Fatalf("classification = %+v, want Fallback", c)
	}
}

// TestProjectClassifierChargeFailureStopsBeforeTheModelCall: a gate denial
// (rate cap reached) never reaches the backend.
func TestProjectClassifierChargeFailureStopsBeforeTheModelCall(t *testing.T) {
	fb := backend.NewFake("fake")
	pc := &ProjectClassifier{Projects: testProjects(), Backend: fb, Model: "haiku", Charge: func() error { return errors.New("rate cap reached") }}
	_, err := pc.Classify(context.Background(), classifyItem("something"))
	if err == nil {
		t.Fatal("expected the charge's error")
	}
	if fb.Calls() != 0 {
		t.Fatalf("backend calls = %d, want 0 (charge must run before the model call)", fb.Calls())
	}
}

// TestProjectClassifierRefusesWithNoProjects: an empty candidate list is a
// caller error, not a silent "always fallback".
func TestProjectClassifierRefusesWithNoProjects(t *testing.T) {
	fb := backend.NewFake("fake")
	pc := &ProjectClassifier{Backend: fb, Model: "haiku"}
	if _, err := pc.Classify(context.Background(), classifyItem("something")); err == nil {
		t.Fatal("expected an error with no candidate projects")
	}
}
