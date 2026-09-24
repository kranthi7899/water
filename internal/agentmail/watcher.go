package agentmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"water/internal/approvals"
	"water/internal/connectors/google/gapi"
	"water/internal/connectors/google/gmail"
	"water/internal/decisions"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/vault"
)

// rawMessage and rawListOutput mirror gmail's list_messages JSON output
// shape exactly (gmail's own message/listMessagesOutput types), decoded
// independently since those are unexported: JSON decoding only cares about
// field tags, not Go type identity, so this needs no change to gmail.go.
type rawMessage struct {
	ID           string   `json:"id"`
	ThreadID     string   `json:"threadId"`
	From         string   `json:"from"`
	To           []string `json:"to"`
	Subject      string   `json:"subject"`
	Body         string   `json:"body"`
	InternalDate string   `json:"internalDate"`
}

type rawListOutput struct {
	Messages  []rawMessage `json:"messages"`
	HistoryID string       `json:"history_id,omitempty"`
}

// Signal is one agent-directed message surfaced to the morning brief: just
// enough to name it, never the full body -- like every other brief signal,
// the model only ever sees this fixed, code-built block (internal/runtime/
// brief.go), never the raw message.
type Signal struct {
	From    string    `json:"from"`
	Subject string    `json:"subject"`
	SeenAt  time.Time `json:"seen_at"`
}

// Defaults for Config's optional fields.
const (
	DefaultFunction   = ConnectorName + ".list_messages"
	DefaultCursorKey  = "agentmail:history_id"
	DefaultSignalKey  = "agentmail:agent_directed"
	DefaultMaxSignals = 20
	defaultMailQuery  = "newer_than:1d"
)

// Config configures a Watcher. Gate, Store, Vault, Approvals and Classifier
// are required.
type Config struct {
	Gate       *gate.Gate
	Store      *store.Store
	Vault      vault.Vault
	Approvals  *approvals.Queue
	Classifier decisions.Classifier

	// ForwardTo is the CEO's own real address a CEO-directed agent-mailbox
	// message is forwarded to once approved (config's agent.forward_to).
	// Empty means CEO-directed mail is logged but never staged: there is
	// nowhere configured to send it yet.
	ForwardTo string

	// Function, CursorKey, SignalKey and MaxSignals default to the
	// package's Default* constants; tests point Function at a fake
	// connector's registered id instead.
	Function   string
	CursorKey  string
	SignalKey  string
	MaxSignals int

	Now  func() time.Time
	Logf func(format string, args ...any)
}

func (c *Config) setDefaults() {
	if c.Function == "" {
		c.Function = DefaultFunction
	}
	if c.CursorKey == "" {
		c.CursorKey = DefaultCursorKey
	}
	if c.SignalKey == "" {
		c.SignalKey = DefaultSignalKey
	}
	if c.MaxSignals <= 0 {
		c.MaxSignals = DefaultMaxSignals
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Logf == nil {
		c.Logf = func(string, ...any) {}
	}
}

// Watcher polls the agent's own mailbox on its own tick (Tick), wired into
// internal/sync.Refresher.Run as a third, independent loop next to the
// existing mail/events ticks.
type Watcher struct{ cfg Config }

// NewWatcher builds a Watcher, filling in defaults.
func NewWatcher(cfg Config) *Watcher {
	cfg.setDefaults()
	return &Watcher{cfg: cfg}
}

// Tick runs one poll: an incremental list since the last processed
// history_id (or a full recent fetch the first time), triages each new
// message, then persists the next cursor. Like sync.Refresher's own ticks,
// it never errors outward -- every failure is logged and simply retried
// next tick, since this runs unattended with no caller to report to.
func (w *Watcher) Tick(ctx context.Context) {
	if _, err := w.cfg.Vault.Get(gapi.Service, Account); err != nil {
		w.cfg.Logf("agentmail: skipping, agent mailbox is not connected (%v)", err)
		return
	}
	cursor, err := w.loadCursor(ctx)
	if err != nil {
		w.cfg.Logf("agentmail: reading cursor: %v", err)
		return
	}
	args := map[string]any{"query": defaultMailQuery}
	if cursor != "" {
		args = map[string]any{"since_history_id": cursor}
	}
	res, err := w.cfg.Gate.Invoke(ctx, gate.Call{Function: w.cfg.Function, Args: args, Origin: gate.P2, Taint: gate.Clean})
	if err != nil {
		if cursor != "" && errors.Is(err, gmail.ErrHistoryTooOld) {
			w.cfg.Logf("agentmail: cursor expired, clearing %s for a full resync", w.cfg.CursorKey)
			if derr := w.cfg.Store.DeleteCursor(ctx, w.cfg.CursorKey); derr != nil {
				w.cfg.Logf("agentmail: clearing cursor: %v", derr)
			}
			return
		}
		w.cfg.Logf("agentmail: %v", err)
		return
	}
	var out rawListOutput
	if err := json.Unmarshal(res.Output, &out); err != nil {
		w.cfg.Logf("agentmail: decoding output: %v", err)
		return
	}
	for _, m := range out.Messages {
		w.triage(ctx, m)
	}
	if out.HistoryID != "" {
		if err := w.cfg.Store.SetCursor(ctx, w.cfg.CursorKey, out.HistoryID); err != nil {
			w.cfg.Logf("agentmail: persisting cursor: %v", err)
		}
	}
}

func (w *Watcher) loadCursor(ctx context.Context) (string, error) {
	v, ok, err := w.cfg.Store.GetCursor(ctx, w.cfg.CursorKey)
	if err != nil || !ok {
		return "", err
	}
	return v, nil
}

// triage classifies one message and either records it as an agent-directed
// signal or stages a forward for the CEO. A classification failure is
// logged and the message is simply skipped this tick: since messages are
// drawn from an incremental Gmail cursor (not re-listed once the cursor
// advances past them), this is a deliberate simplicity/completeness
// trade-off for a background triage feed, the same one sync.Refresher's
// own ticks already make for an ordinary failed connector call.
func (w *Watcher) triage(ctx context.Context, m rawMessage) {
	item := &store.Message{From: m.From, Subject: m.Subject, Body: m.Body}
	c, err := w.cfg.Classifier.Classify(ctx, item)
	if err != nil {
		w.cfg.Logf("agentmail: classifying %q: %v", m.ID, err)
		return
	}
	if !c.NeedsDecision {
		w.recordSignal(ctx, m)
		return
	}
	w.stageForward(ctx, m)
}

func (w *Watcher) recordSignal(ctx context.Context, m rawMessage) {
	sigs, err := RecentSignals(ctx, w.cfg.Store, w.cfg.SignalKey)
	if err != nil {
		w.cfg.Logf("agentmail: reading signals: %v", err)
	}
	sigs = append(sigs, Signal{From: m.From, Subject: m.Subject, SeenAt: w.cfg.Now().UTC()})
	if len(sigs) > w.cfg.MaxSignals {
		sigs = sigs[len(sigs)-w.cfg.MaxSignals:]
	}
	b, err := json.Marshal(sigs)
	if err != nil {
		w.cfg.Logf("agentmail: encoding signals: %v", err)
		return
	}
	if err := w.cfg.Store.SetCursor(ctx, w.cfg.SignalKey, string(b)); err != nil {
		w.cfg.Logf("agentmail: persisting signals: %v", err)
	}
}

// RecentSignals reads the rolling agent-directed signal list back, oldest
// first -- internal/runtime/brief.go's new signal reads this. A missing or
// unreadable record reads as empty rather than an error: this is an
// optional, best-effort signal, like the brief's own open-cards signal.
func RecentSignals(ctx context.Context, st *store.Store, key string) ([]Signal, error) {
	if key == "" {
		key = DefaultSignalKey
	}
	v, ok, err := st.GetCursor(ctx, key)
	if err != nil || !ok || v == "" {
		return nil, err
	}
	var sigs []Signal
	if err := json.Unmarshal([]byte(v), &sigs); err != nil {
		return nil, nil // a corrupt record reads as empty, not a failure
	}
	return sigs, nil
}

// stageForward proposes a level-A gmail.send_message approval. Forwarding
// is deliberately not a new connector function -- see the package doc and
// docs/EVOLUTION_PLAN.md's dated entry for why -- it is exactly a
// send_message call quoting the original message, to the CEO's own real
// address. Origin is P1 ("a task the CEO approved or scheduled"), never P2:
// this envelope is executed later, after the CEO says yes, and gate.go's
// P2 rule ("auto mode never performs outward actions") must never be able
// to apply to it. Nothing is sent until then, through the normal approval
// flow (internal/approvals), exactly like any other A-level call.
func (w *Watcher) stageForward(ctx context.Context, m rawMessage) {
	if w.cfg.ForwardTo == "" {
		w.cfg.Logf("agentmail: %q looks meant for the CEO but agent.forward_to is not configured; not staged", m.ID)
		return
	}
	body := fmt.Sprintf("Forwarded from the agent's own mailbox (addressed to it, not you) --\n\nFrom: %s\nSubject: %s\n\n%s",
		m.From, m.Subject, m.Body)
	payload := map[string]any{"to": []string{w.cfg.ForwardTo}, "subject": "Fwd: " + m.Subject, "body": body}
	env, err := w.cfg.Approvals.Propose(ctx, approvals.Envelope{
		Action: "gmail.send_message", Recipient: w.cfg.ForwardTo, Payload: payload, Origin: string(gate.P1), Risk: "high",
	})
	if err != nil {
		w.cfg.Logf("agentmail: staging forward for %q: %v", m.ID, err)
		return
	}
	w.cfg.Logf("agentmail: staged forward %s for approval (%s)", env.ID, m.ID)
}
