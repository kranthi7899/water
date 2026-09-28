package gateway

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"water/internal/backend"
	"water/internal/store"
)

// TestRecapOnStopWiresProjectClassifierAndPersistsStructuredSignals is
// docs/slices/UI.md Phase 3d's core wiring test: stopping a meeting with a
// project seeded in the roster produces a labelled project guess and a
// structured recap_signals block (with a resolved owner avatar for an
// exact roster name match), and never files a for_project link on its own.
func TestRecapOnStopWiresProjectClassifierAndPersistsStructuredSignals(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.st.Upsert(ctx, &store.Project{
		Meta: store.Meta{Source: "seed", SourceID: "halcyon-rollout"}, Name: "Halcyon rollout",
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.Upsert(ctx, &store.Person{
		Meta: store.Meta{Source: "seed", SourceID: "priya"}, Name: "Priya",
	}); err != nil {
		t.Fatal(err)
	}

	h.fake.Reply = func(req backend.Request) string {
		if strings.Contains(req.Prompt, "<meeting>") {
			// The project-guess call.
			return `{"project_id":"halcyon-rollout","confidence":0.82}`
		}
		// The recap's own phrasing call.
		return "Decisions: moved the extra brokers to spot instances."
	}

	id := h.startMeeting(t, `{}`)
	for _, seg := range []string{
		`{"at":"2026-09-24T15:00:00Z","channel":"mic","text":"we decided to move the extra brokers to spot instances"}`,
		`{"at":"2026-09-24T15:01:00Z","channel":"system","text":"Priya will send the updated budget deck by Friday"}`,
		`{"at":"2026-09-24T15:02:00Z","channel":"mic","text":"what does legal think about the vendor contract?"}`,
		`{"at":"2026-09-24T15:03:00Z","channel":"system","text":"fyi the offsite moved to the east building"}`,
	} {
		if code := h.status(t, "/v1/meetings/"+id+"/segments", seg); code != http.StatusOK {
			t.Fatalf("segment: status = %d", code)
		}
	}
	if code := h.status(t, "/v1/meetings/"+id+"/stop", `{}`); code != http.StatusOK {
		t.Fatalf("stop: status = %d", code)
	}
	h.d.bg.Wait() // wait for the background recap goroutine to finish

	var mv meetingView
	decodeInto(t, do(t, h.srv.URL, "GET", "/v1/meetings/"+id, "", h.token), http.StatusOK, &mv)
	if mv.Recap != recapReady {
		t.Fatalf("recap = %q, want ready (recap_error=%q)", mv.Recap, mv.RecapError)
	}

	if !mv.ProjectGuess.Available || mv.ProjectGuess.ProjectID != "halcyon-rollout" || mv.ProjectGuess.ProjectName != "Halcyon rollout" {
		t.Fatalf("project guess = %+v", mv.ProjectGuess)
	}
	if want := "likely: Halcyon rollout (high)"; mv.ProjectGuess.Label != want {
		t.Fatalf("project guess label = %q, want %q", mv.ProjectGuess.Label, want)
	}

	if mv.RecapSignals == nil {
		t.Fatal("recap_signals was not returned")
	}
	if len(mv.RecapSignals.Decisions) != 1 {
		t.Fatalf("decisions = %+v, want 1", mv.RecapSignals.Decisions)
	}
	if len(mv.RecapSignals.ActionItems) != 1 || mv.RecapSignals.ActionItems[0].Owner != "Priya" {
		t.Fatalf("action items = %+v, want one owned by Priya", mv.RecapSignals.ActionItems)
	}
	if got, want := mv.RecapSignals.ActionItems[0].OwnerInitials, Initials("Priya"); got != want {
		t.Fatalf("owner initials = %q, want %q", got, want)
	}
	if len(mv.RecapSignals.OpenQuestions) != 1 {
		t.Fatalf("open questions = %+v, want 1", mv.RecapSignals.OpenQuestions)
	}
	if len(mv.RecapSignals.FYI) != 1 {
		t.Fatalf("fyi = %+v, want 1", mv.RecapSignals.FYI)
	}

	// The central safety property: a labelled guess never files a link.
	links, err := h.st.LinksFrom(ctx, "meeting", id, store.LinkForProject)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 0 {
		t.Fatalf("for_project links after recap = %+v, want none", links)
	}
}

// TestRecapProjectGuessOwnerWithNoRosterMatchHasNoAvatar: an action item
// whose extracted owner name matches nobody in the roster renders with no
// avatar rather than a fuzzy guess.
func TestRecapProjectGuessOwnerWithNoRosterMatchHasNoAvatar(t *testing.T) {
	h := newHarness(t)
	h.fake.Reply = func(backend.Request) string { return "Decisions: none." }
	id := h.startMeeting(t, `{}`)
	if code := h.status(t, "/v1/meetings/"+id+"/segments", `{"channel":"system","text":"Zorblax will send the deck"}`); code != http.StatusOK {
		t.Fatalf("segment: status = %d", code)
	}
	if code := h.status(t, "/v1/meetings/"+id+"/stop", `{}`); code != http.StatusOK {
		t.Fatalf("stop: status = %d", code)
	}
	h.d.bg.Wait()

	var mv meetingView
	decodeInto(t, do(t, h.srv.URL, "GET", "/v1/meetings/"+id, "", h.token), http.StatusOK, &mv)
	if mv.RecapSignals == nil || len(mv.RecapSignals.ActionItems) != 1 {
		t.Fatalf("recap signals = %+v", mv.RecapSignals)
	}
	if got := mv.RecapSignals.ActionItems[0].OwnerInitials; got != "" {
		t.Fatalf("owner initials = %q, want empty (no exact roster match for %q)", got, mv.RecapSignals.ActionItems[0].Owner)
	}
	// No project seeded at all: the guess must report unavailable, never a
	// blind guess.
	if mv.ProjectGuess.Available || mv.ProjectGuess.Label != "Project match: unavailable" {
		t.Fatalf("project guess = %+v", mv.ProjectGuess)
	}
}

// TestUpcomingMeetingsOnlyReturnsFutureEvents is docs/slices/UI.md Phase
// 3d's ?upcoming=1 contract: only future events from the events table, not
// past ones, and not meeting_sessions rows.
func TestUpcomingMeetingsOnlyReturnsFutureEvents(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, e := range []store.Event{
		{Meta: store.Meta{Source: "gcal", SourceID: "past"}, Title: "Yesterday's standup", StartAt: now.Add(-24 * time.Hour), EndAt: now.Add(-23 * time.Hour)},
		{Meta: store.Meta{Source: "gcal", SourceID: "soon"}, Title: "Board sync", StartAt: now.Add(2 * time.Hour), EndAt: now.Add(3 * time.Hour)},
		{Meta: store.Meta{Source: "gcal", SourceID: "far"}, Title: "Next quarter's offsite", StartAt: now.Add(60 * 24 * time.Hour), EndAt: now.Add(61 * 24 * time.Hour)},
	} {
		if err := h.st.Upsert(ctx, &e); err != nil {
			t.Fatal(err)
		}
	}
	// A meeting session exists too; it must not show up in the upcoming
	// list at all (it's a different shape entirely).
	h.startMeeting(t, `{}`)

	var out []upcomingMeetingView
	decodeInto(t, do(t, h.srv.URL, "GET", "/v1/meetings?upcoming=1", "", h.token), http.StatusOK, &out)
	if len(out) != 1 || out[0].EventID != "soon" {
		t.Fatalf("upcoming = %+v, want exactly the one event within the window", out)
	}
}
