package decider

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"water/internal/decisions"
	"water/internal/store"
	"water/internal/twins"
)

// stubClassifier is a decisions.Classifier double that records whether it
// was ever called, so tests can prove Wrap either does or doesn't reach it.
type stubClassifier struct {
	c      decisions.Classification
	err    error
	called bool
}

func (s *stubClassifier) Classify(context.Context, store.Record) (decisions.Classification, error) {
	s.called = true
	return s.c, s.err
}

// stubDecider is a Decider double whose answer/error is fixed by the test,
// recording the Request it was asked so tests can check Options/Kind.
type stubDecider struct {
	ans    Answer
	err    error
	called bool
	got    Request
}

func (s *stubDecider) Decide(ctx context.Context, r Request) (Answer, error) {
	s.called = true
	s.got = r
	return s.ans, s.err
}

const deciderTestManifest = `id: t
name: T
usage: {window: 1h, model_calls: 50}
connectors:
  - name: gmail
    functions:
      - {name: list_messages, level: R}
`

const deciderTestType = `id: budget
title: Budget request
trigger: someone is asking for money
needs:
  - name: related
    fetch: gmail.list_messages
    kind: lookup
default_rule: escalate to the CEO
severity_weight: 1
`

func testRegistry(t *testing.T) *decisions.Registry {
	t.Helper()
	m, err := twins.Parse([]byte(deciderTestManifest))
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{"twins/t/decisions/budget.yaml": &fstest.MapFile{Data: []byte(deciderTestType)}}
	reg, err := decisions.LoadRegistry(fsys, m)
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func testMessage() *store.Message {
	return &store.Message{
		Meta:    store.Meta{Source: "gmail", SourceID: "1"},
		From:    "a@b.com",
		Subject: "money",
		Body:    "please send funds",
	}
}

// TestWrapNullReturnsFallbackUnchanged is the central R-24 guarantee: with
// no decider configured (Null{}), Wrap must return the exact fallback
// value, not a wrapping struct around it. Interface equality here proves
// object identity, which in turn proves Classify calls never reach Null{}
// at all — even a Null whose Decide panicked would never be exercised,
// since Wrap short-circuits before ever calling it.
func TestWrapNullReturnsFallbackUnchanged(t *testing.T) {
	fb := &stubClassifier{c: decisions.Classification{TypeID: "generic"}}
	reg := testRegistry(t)

	got := Wrap(Null{}, fb, reg)

	if got != decisions.Classifier(fb) {
		t.Fatalf("Wrap(Null{}, fb, reg) returned a different value than fb; got %#v", got)
	}
	if gotPtr, ok := got.(*stubClassifier); !ok || gotPtr != fb {
		t.Fatalf("Wrap(Null{}, fb, reg) = %T, want exactly *stubClassifier(fb)", got)
	}
}

// TestWrapNilDeciderReturnsFallbackUnchanged is the same guarantee for a
// nil Decider, which Wrap must treat identically to Null{}.
func TestWrapNilDeciderReturnsFallbackUnchanged(t *testing.T) {
	fb := &stubClassifier{c: decisions.Classification{TypeID: "generic"}}

	got := Wrap(nil, fb, nil)

	if got != decisions.Classifier(fb) {
		t.Fatalf("Wrap(nil, fb, nil) returned a different value than fb; got %#v", got)
	}
}

// TestWrapNonNullDeciderHonorsSuccessfulChoice proves the other half of
// Wrap: a real (here, stub) Decider that answers is asked, and its choice
// is used, without ever touching the fallback.
func TestWrapNonNullDeciderHonorsSuccessfulChoice(t *testing.T) {
	reg := testRegistry(t)
	fb := &stubClassifier{c: decisions.Classification{TypeID: "generic"}}
	d := &stubDecider{ans: Answer{Choice: "budget", Confidence: 0.75}}

	c := Wrap(d, fb, reg)
	if _, ok := c.(*Classifier); !ok {
		t.Fatalf("Wrap with a non-null decider returned %T, want *decider.Classifier", c)
	}

	got, err := c.Classify(context.Background(), testMessage())
	if err != nil {
		t.Fatalf("Classify error: %v", err)
	}
	if !d.called {
		t.Fatal("the decider was never asked")
	}
	if fb.called {
		t.Fatal("the fallback must not run when the decider answers successfully")
	}
	if !got.NeedsDecision || got.TypeID != "budget" || got.Confidence != 0.75 {
		t.Fatalf("Classify = %+v, want {NeedsDecision:true TypeID:budget Confidence:0.75}", got)
	}
	if d.got.Kind != Choice {
		t.Fatalf("Request.Kind = %v, want Choice", d.got.Kind)
	}
	found := false
	for _, o := range d.got.Options {
		if o == "budget" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Request.Options = %v, want it to include the registry's own type id", d.got.Options)
	}
}

// TestWrapFallsBackOnErrUnavailable is the documented behavior for a
// decider that declines: the fallback's own verdict wins, and the call
// does not fail.
func TestWrapFallsBackOnErrUnavailable(t *testing.T) {
	reg := testRegistry(t)
	fb := &stubClassifier{c: decisions.Classification{NeedsDecision: true, TypeID: "generic", Confidence: 0.3}}
	d := &stubDecider{err: ErrUnavailable}

	c := Wrap(d, fb, reg)
	got, err := c.Classify(context.Background(), testMessage())
	if err != nil {
		t.Fatalf("Classify error: %v", err)
	}
	if !fb.called {
		t.Fatal("the fallback was never asked")
	}
	if got != fb.c {
		t.Fatalf("Classify = %+v, want the fallback's own verdict %+v", got, fb.c)
	}
}

// TestWrapFallsBackOnAnyOtherError proves the fallback also runs for a
// decider error that isn't ErrUnavailable — the spec calls for "ErrUnavailable
// or any other error" to fall back, not fail.
func TestWrapFallsBackOnAnyOtherError(t *testing.T) {
	reg := testRegistry(t)
	fb := &stubClassifier{c: decisions.Classification{NeedsDecision: false, TypeID: "generic"}}
	d := &stubDecider{err: errors.New("boom")}

	c := Wrap(d, fb, reg)
	got, err := c.Classify(context.Background(), testMessage())
	if err != nil {
		t.Fatalf("Classify error: %v", err)
	}
	if !fb.called {
		t.Fatal("the fallback was never asked")
	}
	if got != fb.c {
		t.Fatalf("Classify = %+v, want the fallback's own verdict %+v", got, fb.c)
	}
}

// TestWrapUnknownChoiceFallsBackToGeneric proves a choice the registry
// doesn't recognize is treated as generic, not passed through unchecked.
func TestWrapUnknownChoiceFallsBackToGeneric(t *testing.T) {
	reg := testRegistry(t)
	fb := &stubClassifier{}
	d := &stubDecider{ans: Answer{Choice: "not_a_real_type"}}

	c := Wrap(d, fb, reg)
	got, err := c.Classify(context.Background(), testMessage())
	if err != nil {
		t.Fatalf("Classify error: %v", err)
	}
	if got.TypeID != decisions.GenericID {
		t.Fatalf("Classify.TypeID = %q, want %q for an unrecognized choice", got.TypeID, decisions.GenericID)
	}
}
