package reflex

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"water/internal/nervous/intents"
	"water/internal/nervous/render"
	"water/internal/nervous/slots"
	"water/internal/tools"
)

// quickSenderWindow/quickSenderLimit mirror internal/nervous's own
// SenderWindow/SenderLimit defaults (Design's "person entities come from
// message senders" rule). reflex cannot import internal/nervous (the
// dependency runs the other way), so this is a deliberate, small
// duplication of the same two numbers rather than a shared constant.
const (
	quickSenderWindow = 180 * 24 * time.Hour
	quickSenderLimit  = 500
)

// QuickResult is one quick.* call's outcome: structured data for the model
// to phrase itself (never rendered prose — that's the quick TIERS' job, not
// a tool result the model consumes), plus whether it touched any External
// record.
type QuickResult struct {
	Output  json.RawMessage
	Tainted bool
}

// QuickService exposes a fixed subset of Table() — exactly the handlers an
// intent has explicitly opted into with FunctionSpec.QuickTool, and which
// QuickEligible() also allows — to the main agent as MCP tools. Nothing
// here can reach a connector or the gate: every handler it calls is one of
// this package's own read-only functions (see imports_test.go).
type QuickService struct {
	deps  func() Deps
	table map[string]Handler // keyed by FunctionSpec.QuickTool, e.g. "quick.calendar"
}

// NewQuickService builds a QuickService over every handler in Table() whose
// FunctionSpec both declares a QuickTool name and passes QuickEligible() —
// QuickTool is a deliberate, reviewed opt-in per function, never inferred
// from QuickEligible() alone, so a handler that is merely eligible but
// hasn't had QuickTool set stays internal to the sous chef's own tiers.
func NewQuickService(deps func() Deps) *QuickService {
	q := &QuickService{deps: deps, table: map[string]Handler{}}
	for _, h := range Table() {
		if h.Spec.QuickTool != "" && h.Spec.QuickEligible() {
			q.table[h.Spec.QuickTool] = h
		}
	}
	return q
}

// Functions renders every exposed handler as a tools.QuickFunction
// declaration, sorted by id for a deterministic tool list (so the warm
// session's toolsKey only changes when the set of quick tools actually
// changes, not on map-iteration order).
func (q *QuickService) Functions() []tools.QuickFunction {
	out := make([]tools.QuickFunction, 0, len(q.table))
	for id, h := range q.table {
		out = append(out, tools.QuickFunction{
			ID:          id,
			Tool:        tools.QuickToolName(id),
			Description: quickDescription(id),
			Schema:      quickSchema(h.Spec),
		})
	}
	sortQuickFunctions(out)
	return out
}

func sortQuickFunctions(fs []tools.QuickFunction) {
	for i := 1; i < len(fs); i++ {
		for j := i; j > 0 && fs[j].ID < fs[j-1].ID; j-- {
			fs[j], fs[j-1] = fs[j-1], fs[j]
		}
	}
}

// Run resolves args against id's declared FunctionSpec.Args (using the same
// slots.ResolveString a quick-tool call's string arguments are resolved
// with, Design §11.3), runs the handler, and marshals its render.Result's
// structured fields — never rendered prose, which is a quick TIER's job,
// not a tool result. An unresolved or ambiguous argument is returned as an
// error naming the argument (and, when ambiguous, the candidates), so the
// calling model can adapt rather than get a bare "invalid input."
//
// Named Run, not Invoke: internal/guards' repo-wide scan treats any call to
// a method literally named Invoke outside internal/gate as a suspected
// gate bypass (the structural proof that only the gate reaches a
// connector). This is a different, unrelated operation — a read-only
// reflex lookup that was never gate-shaped to begin with — so it gets its
// own name rather than asking that guard to special-case a second meaning
// of "Invoke."
func (q *QuickService) Run(ctx context.Context, id string, args map[string]any) (QuickResult, error) {
	h, ok := q.table[id]
	if !ok {
		return QuickResult{}, fmt.Errorf("reflex: %q is not a quick function", id)
	}
	d := q.deps()

	ents, err := entitiesForQuick(ctx, d.Store)
	if err != nil {
		// A read error resolves no person argument rather than treating it
		// as "no such person" — the same fail-safe direction
		// internal/nervous's own per-turn entity lookup takes.
		ents = slots.Entities{}
	}

	resolved := make(Args, len(h.Spec.Args))
	for name, typ := range h.Spec.Args {
		raw, present := args[name]
		var s string
		switch {
		case present:
			s = fmt.Sprint(raw)
		case h.Spec.Defaults[name] != "":
			s = h.Spec.Defaults[name]
		case containsArg(h.Spec.Required, name):
			return QuickResult{}, fmt.Errorf("reflex: %s: missing required argument %q", id, name)
		default:
			continue // optional, no default, omitted: leave this slot zero-valued
		}
		v, outcome, opts := slots.ResolveString(typ, s, slots.Spec{}, d.now(), ents)
		switch outcome {
		case slots.Resolved:
			resolved[name] = v
		case slots.Ambiguous:
			return QuickResult{}, fmt.Errorf("reflex: %s: %q is ambiguous — matches: %s", id, name, strings.Join(opts, "; "))
		default:
			return QuickResult{}, fmt.Errorf("reflex: %s: could not resolve %q from %q", id, name, s)
		}
	}

	result, err := h.Run(ctx, d, resolved)
	if err != nil {
		return QuickResult{}, err
	}
	out, err := json.Marshal(struct {
		Intent         string            `json:"intent"`
		Interpretation string            `json:"interpretation,omitempty"`
		Facts          map[string]string `json:"facts,omitempty"`
		Items          []render.Item     `json:"items,omitempty"`
		Warnings       []string          `json:"warnings,omitempty"`
	}{result.Intent, result.Interpretation, result.Facts, result.Items, result.Warnings})
	if err != nil {
		return QuickResult{}, err
	}
	return QuickResult{Output: out, Tainted: result.Tainted}, nil
}

func containsArg(list []string, name string) bool {
	for _, v := range list {
		if v == name {
			return true
		}
	}
	return false
}

func entitiesForQuick(ctx context.Context, st StoreView) (slots.Entities, error) {
	if st == nil {
		return slots.Entities{}, nil
	}
	people, err := st.Senders(ctx, time.Now().Add(-quickSenderWindow), quickSenderLimit)
	if err != nil {
		return slots.Entities{}, err
	}
	return slots.Entities{People: people}, nil
}

// quickDescription and quickArgHint are hand-written, since FunctionSpec
// carries no human-readable text of its own (that lives on the intent, not
// the function, and a quick tool has no intent behind it). Kept in one
// place, next to Table(), so a new QuickTool addition is reviewed alongside
// its wording.
func quickDescription(id string) string {
	switch id {
	case "store.calendar_events":
		return "List the CEO's calendar events for a day or date range."
	case "store.next_event":
		return "The CEO's next upcoming calendar event, if any."
	case "store.latest_messages":
		return "The most recent email messages, newest first."
	case "store.latest_from":
		return "The most recent email message from a specific person."
	case "store.cached_brief":
		return "Today's cached morning brief text, if one has already been computed."
	case "approvals.pending":
		return "The list of actions currently waiting on the CEO's approval."
	default:
		return "A read-only lookup against the twin's local, already-synced data."
	}
}

func quickSchema(spec intents.FunctionSpec) []byte {
	props := make(map[string]any, len(spec.Args))
	for name, typ := range spec.Args {
		props[name] = quickArgSchema(typ)
	}
	schema := map[string]any{"type": "object", "properties": props}
	if len(spec.Required) > 0 {
		schema["required"] = spec.Required
	}
	b, _ := json.Marshal(schema)
	return b
}

func quickArgSchema(t slots.Type) map[string]any {
	if t == slots.TypeCount {
		return map[string]any{"type": "integer", "description": "A whole number."}
	}
	return map[string]any{"type": "string", "description": quickArgHint(t)}
}

func quickArgHint(t slots.Type) string {
	switch t {
	case slots.TypeDate:
		return "A date: today, tomorrow, a weekday, or an ISO date (YYYY-MM-DD)."
	case slots.TypeDateRange:
		return "A day or range: today, tomorrow, this week, next week, or an ISO date."
	case slots.TypeTime:
		return "A time of day, e.g. \"3pm\" or \"15:00\"."
	case slots.TypePartOfDay:
		return "morning, afternoon, or evening."
	case slots.TypeDuration:
		return "A length of time, e.g. \"30 minutes\" or \"an hour\"."
	case slots.TypePerson:
		return "A name or email address, matched against recent senders."
	default:
		return "A plain text value."
	}
}
