package meetings

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"water/internal/backend"
	"water/internal/decisions"
	"water/internal/store"
)

// scriptedTranscript is a fixture meeting: two channels, a decision, an
// owned action item, an unassigned action item, an open question and an
// FYI, plus plain chatter that should surface nowhere in the recap.
func scriptedTranscript(base time.Time) []Segment {
	return []Segment{
		{At: base, Channel: Mic, Text: "let's kick off the compute review"},
		{At: base.Add(1 * time.Minute), Channel: System, Text: "the Kafka budget is over by twenty percent"},
		{At: base.Add(2 * time.Minute), Channel: Mic, Text: "we decided to move the extra brokers to spot instances"},
		{At: base.Add(3 * time.Minute), Channel: System, Text: "Priya will send the updated budget deck by Friday"},
		{At: base.Add(4 * time.Minute), Channel: System, Text: "action item: rotate the staging credentials"},
		{At: base.Add(5 * time.Minute), Channel: Mic, Text: "what does legal think about the vendor contract?"},
		{At: base.Add(6 * time.Minute), Channel: System, Text: "fyi the offsite moved to the east building"},
		{At: base.Add(7 * time.Minute), Channel: Mic, Text: "anyway, how's everyone's weekend"},
	}
}

func TestExtractSignalsBucketsAndTracesToSegmentText(t *testing.T) {
	base := time.Date(2026, 9, 24, 15, 0, 0, 0, time.UTC)
	segs := scriptedTranscript(base)
	sig := ExtractSignals(segs)

	if len(sig.Decisions) != 1 || sig.Decisions[0].Text != segs[2].Text {
		t.Fatalf("decisions = %+v", sig.Decisions)
	}
	if len(sig.ActionItems) != 2 {
		t.Fatalf("action items = %+v, want 2", sig.ActionItems)
	}
	if sig.ActionItems[0].Text != segs[3].Text || sig.ActionItems[0].Owner != "Priya" {
		t.Fatalf("action item 0 = %+v, want owner Priya", sig.ActionItems[0])
	}
	if sig.ActionItems[0].Level != "D" {
		t.Fatalf("action item level = %q, want D (draft only, never executed)", sig.ActionItems[0].Level)
	}
	if sig.ActionItems[1].Text != segs[4].Text || sig.ActionItems[1].Owner != "" {
		t.Fatalf("action item 1 = %+v, want no owner", sig.ActionItems[1])
	}
	if len(sig.OpenQuestions) != 1 || sig.OpenQuestions[0].Text != segs[5].Text {
		t.Fatalf("open questions = %+v", sig.OpenQuestions)
	}
	if len(sig.FYI) != 1 || sig.FYI[0].Text != segs[6].Text {
		t.Fatalf("fyi = %+v", sig.FYI)
	}

	// Property: every extracted item, whatever its bucket, is the exact text
	// of some real segment — never a paraphrase or an invented fact.
	segText := map[string]bool{}
	for _, s := range segs {
		segText[s.Text] = true
	}
	all := append([]RecapItem{}, sig.Decisions...)
	all = append(all, sig.OpenQuestions...)
	all = append(all, sig.FYI...)
	for _, it := range all {
		if !segText[it.Text] {
			t.Fatalf("item %q does not trace to any real segment", it.Text)
		}
	}
	for _, a := range sig.ActionItems {
		if !segText[a.Text] {
			t.Fatalf("action item %q does not trace to any real segment", a.Text)
		}
	}
}

func TestRenderRecapSignalsLabelsProjectGuessAsAGuessNeverFact(t *testing.T) {
	sig := ExtractSignals(scriptedTranscript(time.Now()))
	withGuess := RenderRecapSignals(sig, ProjectGuess{Available: true, TypeID: "budget_request", Confidence: 0.62})
	if !strings.Contains(withGuess, "GUESS") || !strings.Contains(withGuess, "0.62") || !strings.Contains(withGuess, "budget_request") {
		t.Fatalf("rendered signals missing a clearly labeled guess: %q", withGuess)
	}
	if !strings.Contains(withGuess, "unconfirmed") {
		t.Fatalf("rendered signals must say the match is unconfirmed: %q", withGuess)
	}
	noGuess := RenderRecapSignals(sig, ProjectGuess{})
	if !strings.Contains(noGuess, "unavailable") {
		t.Fatalf("rendered signals with no classifier must say unavailable: %q", noGuess)
	}
}

type fakeClassifier struct {
	c   decisions.Classification
	err error
}

func (f fakeClassifier) Classify(context.Context, store.Record) (decisions.Classification, error) {
	return f.c, f.err
}

func newRecapManager(t *testing.T) (*Manager, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st), st
}

// TestRecapStoresTranscriptRefAndSummaryWithGuessedProject exercises
// Recap end to end against a fixture transcript posted through AddSegment
// (mirroring the replay-mode segments endpoint), asserting: one model call,
// TranscriptRef pointing at the session, Summary holding the phrased text,
// and the project match reported as a guess with its confidence.
func TestRecapStoresTranscriptRefAndSummaryWithGuessedProject(t *testing.T) {
	m, st := newRecapManager(t)
	ctx := context.Background()
	s, err := m.Start(ctx, "evt-1")
	if err != nil {
		t.Fatal(err)
	}
	for _, seg := range scriptedTranscript(time.Now()) {
		if err := m.AddSegment(ctx, s.ID, seg); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Stop(ctx, s.ID); err != nil {
		t.Fatal(err)
	}

	fb := backend.NewFake("fake")
	fb.Reply = func(req backend.Request) string {
		return "Decisions: moved brokers to spot.\nAction items: Priya sends deck."
	}
	cls := fakeClassifier{c: decisions.Classification{NeedsDecision: true, TypeID: "budget_request", Confidence: 0.71}}

	res, err := m.Recap(ctx, s.ID, cls, fb, "haiku", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if fb.Calls() != 1 {
		t.Fatalf("model calls = %d, want exactly 1 (code extracts, one call phrases)", fb.Calls())
	}
	if res.Text == "" {
		t.Fatal("recap text is empty")
	}
	if !res.ProjectGuess.Available || res.ProjectGuess.TypeID != "budget_request" || res.ProjectGuess.Confidence != 0.71 {
		t.Fatalf("project guess = %+v", res.ProjectGuess)
	}
	if got := fb.Requests()[0].Model; got != "haiku" {
		t.Fatalf("model = %q, want the fast tier model passed in", got)
	}

	rec, err := store.Get[store.Meeting, *store.Meeting](ctx, st, "meetings", s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rec.TranscriptRef != s.ID {
		t.Fatalf("TranscriptRef = %q, want the session id (the full transcript already lives in meeting_segments)", rec.TranscriptRef)
	}
	if rec.Summary != res.Text {
		t.Fatalf("Summary = %q, want %q", rec.Summary, res.Text)
	}
	if !rec.External {
		t.Fatal("a meeting record built from untrusted transcript content must be marked External")
	}
}

// TestRecapWithNoClassifierLeavesProjectGuessUnavailable confirms a nil
// classifier never causes Recap to guess blind.
func TestRecapWithNoClassifierLeavesProjectGuessUnavailable(t *testing.T) {
	m, _ := newRecapManager(t)
	ctx := context.Background()
	s, err := m.Start(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.AddSegment(ctx, s.ID, Segment{Channel: Mic, Text: "just a quick sync"}); err != nil {
		t.Fatal(err)
	}
	fb := backend.NewFake("fake")
	res, err := m.Recap(ctx, s.ID, nil, fb, "haiku", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.ProjectGuess.Available {
		t.Fatalf("project guess = %+v, want unavailable with no classifier wired", res.ProjectGuess)
	}
}

func TestRecapUnknownSessionIsNotFound(t *testing.T) {
	m, _ := newRecapManager(t)
	fb := backend.NewFake("fake")
	if _, err := m.Recap(context.Background(), "mtg_nope", nil, fb, "haiku", 0); err == nil {
		t.Fatal("expected an error for an unknown session")
	}
}
