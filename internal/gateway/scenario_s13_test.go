package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"water/internal/backend"
	"water/internal/decisions"
	"water/internal/gate"
	"water/internal/meetings"
	"water/internal/runtime"
	"water/internal/store"
	"water/internal/twins"
)

// fixedClassifier is a test-only decisions.Classifier stand-in: it never
// calls a model, so the recap's project guess is deterministic here without
// pulling in the whole internal/decisions registry.
type fixedClassifier struct {
	c   decisions.Classification
	err error
}

func (f fixedClassifier) Classify(context.Context, store.Record) (decisions.Classification, error) {
	return f.c, f.err
}

// TestScenarioS13ScriptedMeetingHelpAndRecap is docs/slices/M.md's replay
// scenario (section 8): a scripted, timestamped transcript is fed through
// the real HTTP surface (start, many segment posts, two on-demand-help
// turns mid-meeting, stop), exercising the real gate/store/daemon wiring
// end to end with only backend.Fake and a fixed classifier standing in for
// the model and the decision registry — proof the whole pipeline works
// without real audio or a live model. The recap itself is then run the
// same way a fixture-transcript test would (docs/slices/M.md section 7:
// "same code path as fixture transcripts"), reading back the very segments
// just posted over HTTP.
func TestScenarioS13ScriptedMeetingHelpAndRecap(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Local records the recap's project guess and the mid-meeting help
	// questions should be able to reference, seeded ahead of the meeting
	// exactly as section 2's prefetch would have left them.
	if err := h.st.Upsert(ctx, &store.Document{
		Meta:  store.Meta{Source: "gdrive", SourceID: "d1", External: true},
		Title: "Kafka budget FY27", Excerpt: "broker costs and the spot-instance plan",
	}); err != nil {
		t.Fatal(err)
	}

	id := h.startMeeting(t, `{"event_id":"evt-compute-review"}`)

	// The scripted transcript: a decision, two action items with distinct
	// owners (one named, one not), an open question and an FYI, plus plain
	// chatter that should surface in none of the recap's buckets. Segments
	// carry no "at" so each lands at post time (Manager.AddSegment's
	// zero-means-now default), keeping every one of them inside on-demand
	// help's 5-minute recent window for the rest of this test.
	post := func(channel, text string) {
		t.Helper()
		body := `{"channel":"` + channel + `","text":"` + strings.ReplaceAll(text, `"`, `\"`) + `"}`
		if code := h.status(t, "/v1/meetings/"+id+"/segments", body); code != http.StatusOK {
			t.Fatalf("segment %q: status = %d", text, code)
		}
	}

	fastModel := h.d.cfg.Manifest.ModelFor(twins.TierFast)

	// askHelp is one mid-meeting on-demand-help turn (docs/slices/M.md
	// section 4): a real /v1/turns call naming this session's meeting_id,
	// answered on the fast tier from the recent transcript, tainting the
	// session on its own even though the segments themselves already did.
	askHelp := func(prompt, wantInPrompt, reply string) {
		t.Helper()
		h.fake.Reply = func(req backend.Request) string { return reply }
		resp := h.post(t, "/v1/turns", `{"channel":"cli","prompt":"`+prompt+`","meeting_id":"`+id+`"}`, h.token)
		events := readEvents(t, resp)
		if len(events) == 0 || events[len(events)-1].Kind != runtime.EventDone {
			t.Fatalf("help turn %q: events = %+v", prompt, events)
		}
		reqs := h.fake.Requests()
		if len(reqs) == 0 {
			t.Fatalf("help turn %q made no backend request", prompt)
		}
		last := reqs[len(reqs)-1]
		if last.Model != fastModel {
			t.Fatalf("help turn %q: model = %q, want the fast tier %q", prompt, last.Model, fastModel)
		}
		if !strings.Contains(last.Prompt, wantInPrompt) {
			t.Fatalf("help turn %q: prompt missing %q: %q", prompt, wantInPrompt, last.Prompt)
		}
	}

	post("mic", "let's kick off the compute review")
	post("system", "the Kafka budget is over by twenty percent")
	post("mic", "we decided to move the extra brokers to spot instances")

	// Help question 1, mid-meeting: grounded in what was just said.
	askHelp("what did they just say about the budget", "Kafka budget is over by twenty percent", "it's over by twenty percent")

	post("system", "Priya will send the updated budget deck by Friday")
	post("system", "action item: rotate the staging credentials")
	post("mic", "what does legal think about the vendor contract?")

	// Help question 2, later in the same meeting: the window has moved on,
	// and the answer must reflect what's been said since question 1.
	askHelp("who's handling the budget deck", "Priya will send the updated budget deck by Friday", "Priya has it")

	post("system", "fyi the offsite moved to the east building")
	post("mic", "anyway, how's everyone's weekend")

	if got := h.sessionTaint(t); got != gate.Tainted {
		t.Fatal("every posted segment must have tainted the session, whatever the channel")
	}

	// Stop over the real endpoint; a repeat stop stays idempotent.
	if code := h.status(t, "/v1/meetings/"+id+"/stop", `{}`); code != http.StatusOK {
		t.Fatalf("stop: status = %d", code)
	}
	if code := h.status(t, "/v1/meetings/"+id+"/stop", `{}`); code != http.StatusOK {
		t.Fatalf("second stop: status = %d, want 200 (idempotent)", code)
	}

	// The recap: the same code path a fixture-transcript test would use
	// (docs/slices/M.md section 7), reading the segments back from the
	// store the HTTP calls above actually wrote.
	h.fake.Reply = func(req backend.Request) string {
		return "Decisions: moved the extra brokers to spot instances.\n" +
			"Action items: Priya to send the budget deck by Friday; rotate the staging credentials (unassigned).\n" +
			"Open questions: legal's view on the vendor contract.\nFYI: the offsite moved to the east building."
	}
	classifier := fixedClassifier{c: decisions.Classification{NeedsDecision: true, TypeID: "budget_request", Confidence: 0.73}}
	mgr := meetings.New(h.st)
	result, err := mgr.Recap(ctx, id, classifier, h.fake, fastModel, 5*time.Second)
	if err != nil {
		t.Fatalf("recap: %v", err)
	}

	if len(result.Signals.ActionItems) != 2 {
		t.Fatalf("action items = %+v, want exactly 2", result.Signals.ActionItems)
	}
	owners := map[string]bool{}
	for _, a := range result.Signals.ActionItems {
		owners[a.Owner] = true
		// Section 7: nothing becomes a task until the CEO confirms — every
		// recap action item is level D (draft only, nothing executed) by
		// construction.
		if a.Level != twins.D {
			t.Fatalf("action item %+v: level = %q, want D (staged, never executed)", a, a.Level)
		}
	}
	if !owners["Priya"] || !owners[""] {
		t.Fatalf("owners = %+v, want one named (Priya) and one unassigned", owners)
	}
	if len(result.Signals.Decisions) != 1 || len(result.Signals.OpenQuestions) != 1 || len(result.Signals.FYI) != 1 {
		t.Fatalf("signals = %+v, want exactly one decision/open question/FYI", result.Signals)
	}

	if !result.ProjectGuess.Available || result.ProjectGuess.TypeID != "budget_request" {
		t.Fatalf("project guess = %+v, want the fixed classifier's budget_request guess", result.ProjectGuess)
	}

	// The recap's own model call phrased the signal block, and its store
	// side effect landed: TranscriptRef points back at the session itself
	// (the transcript already lives in meeting_segments, not duplicated),
	// and Summary is the phrased text.
	meeting, err := store.Get[store.Meeting](ctx, h.st, "meetings", id)
	if err != nil {
		t.Fatalf("recap meeting record: %v", err)
	}
	if meeting.TranscriptRef != id {
		t.Fatalf("TranscriptRef = %q, want %q", meeting.TranscriptRef, id)
	}
	if meeting.Summary == "" || !strings.Contains(meeting.Summary, "budget deck") {
		t.Fatalf("Summary = %q, missing the phrased action item", meeting.Summary)
	}
	if !meeting.External {
		t.Fatal("a meeting recap is external content, like every other Slice M record")
	}
}
