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
	// EventQueued is informational: this turn is waiting for another turn
	// (from any client or channel) to finish its model call, since only one
	// runs at a time. It is sent at most once, after ack and before any
	// delta; clients that do not know it can ignore it.
	EventQueued EventKind = "queued"
	EventDone   EventKind = "done"
	EventError  EventKind = "error"
	// EventToolStart and EventToolEnd bracket one tool call the model made
	// while this turn's model call was running (docs/slices/V.md §7.4
	// V-events): tool_start when the daemon receives the call, tool_end with
	// its Status once the call's response is written. Both carry the same
	// StepID, the function id (Tool) and a code-built Label, never the
	// model's arguments. They are informational and side-effect free:
	// clients that do not know them ignore them.
	EventToolStart EventKind = "tool_start"
	EventToolEnd   EventKind = "tool_end"
)

// StepStatus is a tool_end event's outcome.
type StepStatus string

const (
	// StepOK: the call ran and returned its output.
	StepOK StepStatus = "ok"
	// StepQueued: the call was proposed as an approval envelope; nothing ran.
	StepQueued StepStatus = "queued"
	// StepDenied: the call was refused, or failed before anything ran.
	StepDenied StepStatus = "denied"
	// StepError: the call ran and something after it failed
	// (executed_with_error), or the handler ended some other way.
	StepError StepStatus = "error"
)

// queuedAfter is how long BeginModel may block before RunTurn tells the
// client the turn is queued behind another one.
const queuedAfter = 250 * time.Millisecond

// Event is one step of a turn.
//
// An approval_required event carries everything a client needs to show and
// decide the queued call without a second request: ApprovalID, Action (also
// repeated in Text for older clients), Risk and PayloadHash, which POST
// /v1/approvals/{id}/decision requires, plus ReadBack: the code-built text
// (approvals.ReadBack) to speak or show before asking yes or no, never
// composed by a model. It is best-effort; see the gateway's handleTurn doc
// comment for exactly which approvals are announced inline.
type Event struct {
	Kind        EventKind `json:"kind"`
	Text        string    `json:"text,omitempty"`
	ApprovalID  string    `json:"approval_id,omitempty"`
	Action      string    `json:"action,omitempty"`
	Risk        string    `json:"risk,omitempty"`
	PayloadHash string    `json:"payload_hash,omitempty"`
	ReadBack    string    `json:"read_back,omitempty"`
	Error       string    `json:"error,omitempty"`
	// StepID, Tool, Label and Status belong to tool_start/tool_end only.
	StepID string     `json:"step_id,omitempty"`
	Tool   string     `json:"tool,omitempty"`
	Label  string     `json:"label,omitempty"`
	Status StepStatus `json:"status,omitempty"`
}

// ApprovalRequiredEvent is the one approval_required shape every path
// emits (the model-queued tool call, a Tier-0 write intent, a spoken yes on
// a tap-required envelope): the id, the action (repeated in Text for older
// clients), risk and payload hash (what POST /v1/approvals/{id}/decision
// needs), and the code-built read-back (approvals.ReadBack).
func ApprovalRequiredEvent(env approvals.Envelope) Event {
	return Event{Kind: EventApprovalRequired, ApprovalID: env.ID, Text: env.Action,
		Action: env.Action, Risk: env.Risk, PayloadHash: env.PayloadHash, ReadBack: approvals.ReadBack(env)}
}

// Env is everything one turn needs. The daemon builds one Env per twin and
// reuses it across turns; Tools, BeginModel and OnTaint are wired per daemon
// by the caller (see internal/gateway's turnEnv).
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
	// the twin's connector functions through the gate. The gateway points it
	// at the daemon's one stable session proxy token, not a per-turn one: its
	// taint is session-sticky (escalated for the daemon's lifetime once any
	// turn, meeting segment or tool result brings in untrusted content).
	Tools *tools.Policy
	// BeginModel, when set, is called after the fast paths and before the
	// turn's state is read and the model is called; the returned end func is
	// called when the turn finishes. The daemon uses it to run one model turn
	// at a time and to know which open turn stream a queued tool call belongs
	// to. An error (the turn was cancelled while waiting) ends the turn with
	// an error event.
	BeginModel func(ctx context.Context) (end func(), err error)
	// Decisions, when set, is run to produce the morning brief's ranked
	// open-cards signal. Nil (no decision registry wired) leaves that signal
	// absent rather than erroring.
	Decisions DecisionSource
	// Timeout bounds one model call.
	Timeout time.Duration
	Now     func() time.Time
	// OnTaint, when set, is called with true whenever the turn pulls in
	// External content: RunTurn calls it when the state summary it hands
	// the model is tainted (before the model sees it), and a fast path calls
	// it when it answers from external content without a model call (today
	// only the morning brief does), so a cached brief built from external
	// mail or events still marks the session tainted for the tool calls that
	// follow it. The daemon wires this to escalateTaint.
	OnTaint func(tainted bool)
	// StyleBlock, when set, is appended to the system prompt (see
	// RoleSystem) so the head chef's own prose follows the same tone and
	// voice rules the quick tiers' renderer uses. It's set once per twin at
	// startup (internal/nervous/render.Style.PromptBlock()), so it stays
	// byte-identical across turns like the rest of the system prompt.
	StyleBlock string
	// MaxChars is style.yaml's max_chars per channel. TurnPrompt states the
	// turn's own channel budget in the channel hint; nothing truncates a
	// model reply to it (docs/slices/V.md D5). Nil leaves the number out.
	MaxChars map[Channel]int
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

// ModelTurn is one turn served by the head chef: it builds the system
// prompt and current-state summary, streams a reply through the backend,
// and returns the assembled response. It makes no fast-path attempt (that
// now lives in internal/nervous's Tier 0, ahead of this call) and emits no
// ack or done event — the caller decides how to frame those, since a turn
// reaching here may already have gone through a quick-tier attempt the
// caller alone knows about. A returned error means no usable reply; the
// caller decides how to surface that (an EventError, in every caller today).
func ModelTurn(ctx context.Context, env Env, turn Turn, emit func(Event)) (backend.Response, error) {
	// The system prompt is the role alone, so it stays byte-identical from
	// turn to turn and the warm session keeps its process and conversation.
	// Live state (today's events, the pending count) changes between turns,
	// so it travels with each turn's message instead.
	if env.BeginModel != nil {
		end, err := beginModelNoting(ctx, env.BeginModel, emit)
		if err != nil {
			return backend.Response{}, err
		}
		defer end()
	}
	// The summary built here is exactly what the model sees, so its taint is
	// applied here too, before the request exists: a caller's own earlier
	// StateSummary may predate a sync that has since stored external content.
	summary, tainted := StateSummary(ctx, env)
	if tainted && env.OnTaint != nil {
		env.OnTaint(true)
	}
	req := backend.Request{
		System:  RoleSystem(env),
		Prompt:  TurnPrompt(env, turn.Channel, summary, turn.Prompt),
		Model:   env.Manifest.ModelFor(twins.TierFast),
		Timeout: env.timeout(),
		Tools:   env.Tools,
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
		return resp, err
	}
	if turn.Channel == ChannelVoice {
		for _, s := range splitter.Flush() {
			emit(Event{Kind: EventSentence, Text: s})
		}
	}
	return resp, nil
}

// RunTurn is a transitional compatibility wrapper: it reproduces the old
// ack+reply+done event shape, but with no keyword fast path — every turn
// goes straight to ModelTurn. It exists only because internal/gateway's
// handleTurn still calls it directly; task R-15 rewires that call to
// internal/nervous's Handle (which tries Tier 0 first and falls back to
// this same ModelTurn), at which point this wrapper is removed. It never
// returns an error: failures are reported as an EventError so the caller's
// stream always ends cleanly with either "done" or "error".
func RunTurn(ctx context.Context, env Env, turn Turn, emit func(Event)) {
	emit(Event{Kind: EventAck})
	resp, err := ModelTurn(ctx, env, turn, emit)
	if err != nil {
		emit(Event{Kind: EventError, Error: err.Error()})
		return
	}
	emit(Event{Kind: EventDone, Text: resp.Text})
}

// beginModelNoting calls begin and, if it is still waiting for the model
// slot after queuedAfter, emits one EventQueued so the client can tell a
// queued turn from a hung one. The event is emitted from a timer goroutine
// while this goroutine is blocked in begin, and beginModelNoting waits for
// that emit to finish before returning, so emit is never called
// concurrently by this turn.
func beginModelNoting(ctx context.Context, begin func(context.Context) (func(), error), emit func(Event)) (func(), error) {
	emitted := make(chan struct{})
	timer := time.AfterFunc(queuedAfter, func() {
		defer close(emitted)
		emit(Event{Kind: EventQueued, Text: "waiting for the previous turn to finish"})
	})
	end, err := begin(ctx)
	if !timer.Stop() {
		<-emitted
	}
	return end, err
}

// streamCold is stream without the warm session, for calls whose system
// prompt or tools differ from the chat's (the morning brief): running them
// on the warm session would restart the chat's process.
func streamCold(ctx context.Context, env Env, req backend.Request, onDelta func(string)) (backend.Response, error) {
	env.Warm = nil
	return stream(ctx, env, req, onDelta)
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

// DeliverText delivers a complete, already-final piece of text as a single
// delta, splitting it into sentence events on the voice channel too. It's
// used by both the RunTurn compatibility wrapper's caller and (from a later
// task on) internal/nervous, for a quick tier's answer or a computed brief:
// text that exists all at once, unlike a model's streamed reply.
func DeliverText(ch Channel, text string, emit func(Event)) {
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

// RoleSystem is the twin's system prompt: role.md (or a placeholder until it
// exists), and nothing that changes between turns.
func RoleSystem(env Env) string {
	role := placeholderRole
	if strings.TrimSpace(env.RoleMD) != "" {
		role = env.RoleMD
	}
	if strings.TrimSpace(env.StyleBlock) == "" {
		return role
	}
	return role + "\n\n## Style\n" + env.StyleBlock
}

// TurnPrompt is one turn's user message: the current state (see
// StateSummary, whose taint the caller applies to the session before the
// turn runs), a one-line channel hint (ChannelHint), then the CEO's
// request. The hint lives here, in the per-turn message, not in the system
// prompt, so the system prompt stays byte-identical across channels and the
// warm session keeps its process.
func TurnPrompt(env Env, ch Channel, summary, prompt string) string {
	var b strings.Builder
	b.WriteString("## Current state (as of " + env.now().Local().Format("15:04") + ")\n" + summary + "\n\n")
	if h := ChannelHint(ch, env.MaxChars[ch]); h != "" {
		b.WriteString(h + "\n\n")
	}
	b.WriteString("## CEO\n" + prompt)
	return b.String()
}

// ChannelHint is the channel line TurnPrompt adds: which channel the reply
// goes to and style.yaml's character budget for it (maxChars <= 0 leaves
// the number out). Only voice asks for short spoken sentences with no
// lists or markdown. An empty channel gets no hint.
func ChannelHint(ch Channel, maxChars int) string {
	if ch == "" {
		return ""
	}
	var parts []string
	if maxChars > 0 {
		parts = append(parts, fmt.Sprintf("reply in at most about %d characters", maxChars))
	}
	if ch == ChannelVoice {
		parts = append(parts, "short spoken sentences, no lists or markdown")
	}
	h := "## Channel: " + string(ch)
	if len(parts) > 0 {
		h += " (" + strings.Join(parts, "; ") + ")"
	}
	return h
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
			fmt.Fprintf(&b, "- %s %s\n", e.Clock(), e.Title)
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
