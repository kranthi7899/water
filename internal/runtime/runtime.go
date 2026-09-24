// Package runtime is the CEO twin's turn loop: it assembles context from the
// role definition, the store and long-term memory, decides whether a
// deterministic fast path can answer without a model call, and otherwise
// streams a reply through the backend. It has no daemon or transport
// concerns; internal/gateway wires this to the socket.
package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"water/internal/approvals"
	"water/internal/backend"
	"water/internal/decisions"
	"water/internal/store"
	"water/internal/tools"
	"water/internal/twins"
)

// DecisionSource supplies the morning brief's ranked-open-cards signal. In
// production this is a *decisions.Trigger (its Run method already matches
// this shape); tests can fake it. decisions never imports runtime, so this
// stays a one-way dependency.
type DecisionSource interface {
	Run(ctx context.Context, now time.Time) ([]*decisions.Card, error)
}

// Channel names a client kind, which changes how a reply is delivered.
type Channel string

const (
	ChannelCLI     Channel = "cli"
	ChannelVoice   Channel = "voice"
	ChannelTextBar Channel = "text-bar"
)

// Turn is one user request.
type Turn struct {
	Channel Channel
	Prompt  string
}

// EventKind names one event streamed to a client over the turn's socket
// connection (NDJSON at the gateway layer).
type EventKind string

const (
	EventAck              EventKind = "ack"
	EventDelta            EventKind = "delta"
	EventSentence         EventKind = "sentence"
	EventApprovalRequired EventKind = "approval_required"
	EventDone             EventKind = "done"
	EventError            EventKind = "error"
)

// Event is one step of a turn.
type Event struct {
	Kind       EventKind `json:"kind"`
	Text       string    `json:"text,omitempty"`
	ApprovalID string    `json:"approval_id,omitempty"`
	Error      string    `json:"error,omitempty"`
}

// Env is everything one turn needs. The daemon builds one Env per twin and
// reuses it across turns; Tools is set fresh per turn (it carries a per-turn
// token for the model's tool calls) by the caller.
type Env struct {
	Manifest  *twins.Manifest
	Store     *store.Store
	Approvals *approvals.Queue
	// RoleMD is the twin's role.md content (its responsibilities and
	// environment). Empty is tolerated: a placeholder system prompt is used
	// until it exists.
	RoleMD string
	// Backend is used when Warm is nil, or as the warm session's crash/cold
	// fallback path already handles itself.
	Backend backend.Backend
	// Warm, when set, is preferred: one persistent process serves every fast
	// tier turn.
	Warm *backend.WarmSession
	// Tools, when set, is handed to the backend request so the model can call
	// the twin's connector functions through the gate (Env.Tools is prepared
	// per turn by the gateway, which mints the per-turn proxy token).
	Tools *tools.Policy
	// Decisions, when set, is run to produce the morning brief's ranked
	// open-cards signal. Nil (no decision registry wired) leaves that signal
	// absent rather than erroring.
	Decisions DecisionSource
	// Timeout bounds one model call.
	Timeout time.Duration
	Now     func() time.Time
	// OnTaint, when set, is called with true whenever a fast path itself
	// pulls in External content while answering without a model call (today
	// only the morning brief does) — the same escalation a normal turn's
	// tainted context gets from handleTurn, so a cached brief built from
	// external mail or events still marks the session tainted for the tool
	// calls that follow it. The daemon wires this to escalateTaint.
	OnTaint func(tainted bool)
}

func (e Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

func (e Env) timeout() time.Duration {
	if e.Timeout > 0 {
		return e.Timeout
	}
	return 60 * time.Second
}

const placeholderRole = "You are the CEO's digital twin. twins/ceo/role.md has not been written yet in this checkout; answer conservatively and say so if asked about responsibilities you have not been told about."

// RunTurn assembles context, tries a fast path, and otherwise streams a
// model reply, delivering every step to emit in order. It never returns an
// error: failures are reported as an EventError so the caller's stream always
// ends cleanly with either "done" or "error".
func RunTurn(ctx context.Context, env Env, turn Turn, emit func(Event)) {
	emit(Event{Kind: EventAck})

	if text, ok := FastPath(ctx, env, turn.Prompt); ok {
		deliverText(turn.Channel, text, emit)
		emit(Event{Kind: EventDone, Text: text})
		return
	}

	sys, _ := AssembleSystem(ctx, env)
	req := backend.Request{
		System:  sys,
		Prompt:  turn.Prompt,
		Model:   env.Manifest.ModelFor(twins.TierFast),
		Timeout: env.timeout(),
		Tools:   env.Tools,
	}
	if turn.Channel == ChannelVoice {
		req.Prompt += "\n\n(Reply briefly, in short spoken sentences.)"
	}

	var splitter SentenceSplitter
	onDelta := func(d string) {
		emit(Event{Kind: EventDelta, Text: d})
		if turn.Channel == ChannelVoice {
			for _, s := range splitter.Feed(d) {
				emit(Event{Kind: EventSentence, Text: s})
			}
		}
	}

	resp, err := stream(ctx, env, req, onDelta)
	if err != nil {
		emit(Event{Kind: EventError, Error: err.Error()})
		return
	}
	if turn.Channel == ChannelVoice {
		for _, s := range splitter.Flush() {
			emit(Event{Kind: EventSentence, Text: s})
		}
	}
	emit(Event{Kind: EventDone, Text: resp.Text})
}

// stream picks the warm session when available, else the backend's own
// Streamer, else falls back to a single buffered Run whose full text is
// delivered as one delta.
func stream(ctx context.Context, env Env, req backend.Request, onDelta func(string)) (backend.Response, error) {
	if env.Warm != nil {
		return env.Warm.RunTurn(ctx, req, onDelta)
	}
	if st, ok := env.Backend.(backend.Streamer); ok {
		return st.RunStream(ctx, req, onDelta)
	}
	resp, err := env.Backend.Run(ctx, req)
	if err == nil && resp.Text != "" {
		onDelta(resp.Text)
	}
	return resp, err
}

func deliverText(ch Channel, text string, emit func(Event)) {
	emit(Event{Kind: EventDelta, Text: text})
	if ch != ChannelVoice {
		return
	}
	var s SentenceSplitter
	for _, sent := range s.Feed(text) {
		emit(Event{Kind: EventSentence, Text: sent})
	}
	for _, sent := range s.Flush() {
		emit(Event{Kind: EventSentence, Text: sent})
	}
}

// AssembleSystem builds the system prompt: role.md, then a compact state
// summary from the store. It reports whether anything it pulled in is
// external/untrusted, so the caller can taint any tool calls the turn makes.
func AssembleSystem(ctx context.Context, env Env) (system string, tainted bool) {
	var b strings.Builder
	if strings.TrimSpace(env.RoleMD) != "" {
		b.WriteString(env.RoleMD)
	} else {
		b.WriteString(placeholderRole)
	}
	summary, tainted := StateSummary(ctx, env)
	b.WriteString("\n\n## Current state\n")
	b.WriteString(summary)
	return b.String(), tainted
}

// StateSummary renders today's events and the pending-approval count, the
// minimal state slice A2 needs; later slices add mail, docs and the brief.
func StateSummary(ctx context.Context, env Env) (summary string, tainted bool) {
	if env.Store == nil {
		return "(no store attached)", false
	}
	now := env.now()
	start := startOfDay(now)
	end := start.Add(24 * time.Hour)
	var b strings.Builder

	todays, err := store.EventsInRange(ctx, env.Store, start, end)
	if err != nil {
		fmt.Fprintf(&b, "Today's events: unavailable (%v)\n", err)
	} else {
		fmt.Fprintf(&b, "Today's events: %d\n", len(todays))
		for _, e := range todays {
			fmt.Fprintf(&b, "- %s %s\n", e.StartAt.Local().Format("15:04"), e.Title)
			tainted = tainted || e.External
		}
	}

	if env.Approvals != nil {
		pend, err := env.Approvals.Pending(ctx)
		if err != nil {
			fmt.Fprintf(&b, "Pending approvals: unavailable (%v)\n", err)
		} else {
			fmt.Fprintf(&b, "Pending approvals: %d\n", len(pend))
		}
	}
	return strings.TrimSpace(b.String()), tainted
}

func startOfDay(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}
