// This file is the research runner itself (docs/slices/UI.md Phase 5c):
// a simple FIFO queue plus one background goroutine that runs research runs
// one at a time, each as five fixed, code-built steps (overview, market,
// competitors, pricing, risks), each step one research.web call through
// Gate.Invoke at origin P1 with a code-templated query
// ("<idea title> <facet>").
//
// Origin P1, not P2, is a deliberate coordinator decision, not the plan
// document's own literal wording (which said P2). P2 additionally requires
// a function to sit on the manifest's auto_allowlist (gate.go's own
// P2-only check), and Slice W had deliberately kept research.web off that
// list, with its own test asserting exactly that. This runner is CEO-
// initiated work (the CEO clicked "Start research" on a specific idea),
// the same shape as the decision classifier's or the meeting recap's own
// P1 model calls -- not autonomous background polling, which is what
// auto_allowlist exists to gate. P1 gets identical rate-cap treatment to
// P2 (gate.go's rateLimitFor treats them the same), so nothing about rate
// limiting or behavior changes; only the origin label does, and Slice W's
// "research.web is never on the auto allowlist" invariant stays intact.
//
// Design, in one place since none of it is visible from any single
// function's own signature:
//
//   - Queueing (queueResearchRun) is a plain slice under d.researchMu, FIFO
//     (append at the back, pop from the front). d.researchRunning tracks
//     whether a worker goroutine is already draining it; queueResearchRun
//     starts exactly one (tracked on d.bg, the same sync.WaitGroup
//     workspace_meetings.go's recap-on-stop already uses, so tests can
//     h.d.bg.Wait() for it) only when none is running, and that goroutine
//     keeps popping and running until the queue is empty, then exits. This
//     gives "one run at a time" and FIFO order for free: a second run
//     queued while the first is still processing simply waits in the slice
//     until the worker gets to it, never starting a second goroutine.
//   - The metered check (backend.Availability.Metered, via
//     d.cfg.Backend.Available(ctx)) runs exactly once, at the very start of
//     runResearch, before the run's status ever moves off "queued" and
//     before any research.web call. There is no per-step re-check: a run
//     already in flight is short (five bounded calls, each well under a
//     minute) and the same "check once per operation" posture
//     workspace_meetings.go's startRecapOnStop already uses for its own
//     gated background model work (one Gate.ModelCall check before the
//     call, not one per phrase). Documented here as the judgment call it
//     is.
//   - Step status is written to research_steps both before a step's
//     research.web call ("running") and after it resolves ("done" or
//     "failed"), so a client polling mid-run sees a real, live "N of 5",
//     never only the final state.
//   - A denial (or any other Gate.Invoke error -- a rate cap, a query the
//     exfiltration guard refused, a runner failure) stops the run at once:
//     the failing step is marked "failed", the run is marked "failed" with
//     whatever partial report the earlier steps produced, and no further
//     step ever runs. There is no retry and no skip-ahead.
//   - The finished report's text is written through
//     store.SetResearchRunReport, whose own row is pinned Untrusted: true
//     by both a CHECK constraint and the Go layer (store.ResearchRun's own
//     doc comment) -- this file never attempts to set it otherwise.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"water/internal/connectors/research"
	"water/internal/gate"
	"water/internal/store"
)

// researchFacets is Phase 5c's own fixed, code-built list of steps: never
// model-chosen, never configurable per twin.
var researchFacets = []string{"overview", "market", "competitors", "pricing", "risks"}

// researchRunTimeout bounds one whole run (five research.web calls, each
// bounded by research.Timeout, plus store writes): comfortably above
// 5*research.Timeout with headroom for a slow store, well under anything a
// client would consider hung.
const researchRunTimeout = 5 * time.Minute

// researchStepQuery is Phase 5c's own code-templated query: "<idea title>
// <facet>" (e.g. "On-device short-utterance model overview"), never
// model-composed. Slice W's exfiltration guard (research.ValidateQuery)
// still runs on this text inside the research connector's own Invoke, same
// as any other research.web call -- this function does not, and must not,
// try to pre-validate or bypass it.
func researchStepQuery(ideaTitle, facet string) string {
	return strings.TrimSpace(ideaTitle) + " " + facet
}

// queueResearchRun enqueues runID (already persisted as a "queued"
// store.ResearchRun row by handleStartIdeaResearch) on this daemon's FIFO
// research queue, starting the single background worker if none is already
// draining it. See this file's own top-of-file doc comment for the full
// design.
func (d *Daemon) queueResearchRun(runID string) {
	d.researchMu.Lock()
	d.researchQueue = append(d.researchQueue, runID)
	startWorker := !d.researchRunning
	if startWorker {
		d.researchRunning = true
	}
	d.researchMu.Unlock()
	if startWorker {
		d.bg.Add(1)
		go d.researchWorker()
	}
}

// researchWorker drains d.researchQueue one run at a time (docs/slices/UI.md
// Phase 5c: "It runs one run at a time in a daemon goroutine"), exiting once
// the queue is empty; queueResearchRun starts a fresh one the next time a
// run is queued.
func (d *Daemon) researchWorker() {
	defer d.bg.Done()
	for {
		d.researchMu.Lock()
		if len(d.researchQueue) == 0 {
			d.researchRunning = false
			d.researchMu.Unlock()
			return
		}
		runID := d.researchQueue[0]
		d.researchQueue = d.researchQueue[1:]
		d.researchMu.Unlock()
		d.runResearch(runID)
	}
}

// researchStepOutput mirrors research.web's own JSON result shape (the
// unexported "output" type in internal/connectors/research) just enough to
// read a step's summary, sources and note back out of gate.Result.Output.
type researchStepOutput struct {
	Summary string            `json:"summary"`
	Sources []research.Source `json:"sources"`
	Note    string            `json:"note,omitempty"`
}

// capitalize upper-cases facet's first rune for the report's section
// headings ("overview" -> "Overview"); facets are always plain ASCII words
// from researchFacets, never external text.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// researchSectionText renders one finished step as a report section: a
// heading, the summary, and its sources (title plus URL) when any came
// back.
func researchSectionText(facet string, out researchStepOutput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n%s\n", capitalize(facet), out.Summary)
	if out.Note != "" {
		fmt.Fprintf(&b, "(%s)\n", out.Note)
	}
	if len(out.Sources) > 0 {
		b.WriteString("Sources:\n")
		for _, s := range out.Sources {
			fmt.Fprintf(&b, "- %s (%s)\n", s.Title, s.URL)
		}
	}
	return b.String()
}

// researchFailureText renders a stopped run's report: whatever earlier
// steps finished, plus the reason the run stopped.
func researchFailureText(sections []string, facet string, err error) string {
	sections = append(sections, fmt.Sprintf("## %s\nStopped: %s\n", capitalize(facet), err.Error()))
	return strings.Join(sections, "\n\n")
}

// runResearch runs one queued run's five fixed steps to completion or to
// the first denial, synchronously (the caller, researchWorker, is this
// daemon's one research goroutine, so "synchronous here" already means "one
// run at a time" for the whole daemon). It always leaves the run in a
// terminal status (finished or failed): there is no path back to "queued"
// or "running" once this returns.
func (d *Daemon) runResearch(runID string) {
	ctx, cancel := context.WithTimeout(context.Background(), researchRunTimeout)
	defer cancel()

	run, err := d.cfg.Store.GetResearchRun(ctx, runID)
	if err != nil {
		return // the run row is gone; nothing to run or to mark
	}

	fail := func(reason string) {
		_ = d.cfg.Store.SetResearchRunReport(ctx, runID, reason)
		_ = d.cfg.Store.UpdateResearchRunStatus(ctx, runID, "failed", d.computeNow())
	}

	// W invariant 5 (docs/slices/W.md), checked exactly once, before the
	// first step and before the run's status ever leaves "queued": this
	// runner is subscription-only and must never make a metered call.
	if d.cfg.Backend == nil {
		fail("refused: no model backend is configured; research is subscription-only")
		return
	}
	if av := d.cfg.Backend.Available(ctx); av.Metered {
		fail("refused: the configured backend reports metered; research runs only on the Claude subscription (W invariant 5), never a paid API call")
		return
	}
	if d.cfg.Gate == nil {
		fail("refused: no gate is configured")
		return
	}

	if err := d.cfg.Store.UpdateResearchRunStatus(ctx, runID, "running", time.Time{}); err != nil {
		return
	}

	var sections []string
	for i, facet := range researchFacets {
		n := i + 1
		_ = d.cfg.Store.UpsertResearchStep(ctx, store.ResearchStep{RunID: runID, N: n, Label: facet, Status: "running"})

		query := researchStepQuery(run.Topic, facet)
		res, err := d.cfg.Gate.Invoke(ctx, gate.Call{
			Function: "research.web",
			Args:     map[string]any{"query": query},
			Origin:   gate.P1,
			Taint:    gate.Clean,
		})
		if err != nil {
			_ = d.cfg.Store.UpsertResearchStep(ctx, store.ResearchStep{RunID: runID, N: n, Label: facet, Status: "failed"})
			fail(researchFailureText(sections, facet, err))
			return
		}

		var out researchStepOutput
		_ = json.Unmarshal(res.Output, &out)
		_ = d.cfg.Store.UpsertResearchStep(ctx, store.ResearchStep{RunID: runID, N: n, Label: facet, Status: "done", SourceCount: len(out.Sources)})
		sections = append(sections, researchSectionText(facet, out))
	}

	_ = d.cfg.Store.SetResearchRunReport(ctx, runID, strings.Join(sections, "\n\n"))
	_ = d.cfg.Store.UpdateResearchRunStatus(ctx, runID, "finished", d.computeNow())
}
