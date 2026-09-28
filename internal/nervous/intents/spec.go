package intents

import "water/internal/nervous/slots"

// Class buckets what a function computes, for the capability assessment a
// learned-intent promotion needs (Slice R's promotion loop, a later task).
type Class string

const (
	ClassLookup  Class = "lookup"
	ClassCompute Class = "compute"
	ClassFormat  Class = "format"
	ClassControl Class = "control"
	ClassAction  Class = "action"
)

// FunctionSpec is a reflex handler's or proposer's declared contract: its
// argument shape, and the properties LoadRegistry and the promotion loop
// need to decide whether an intent may target it, and whether it may ever
// be exposed as a quick tool or learned.
type FunctionSpec struct {
	ID       string
	Args     map[string]slots.Type
	Required []string
	// Defaults are used when a quick-tool call omits an optional argument.
	Defaults map[string]string

	Class         Class
	ReadOnly      bool
	Deterministic bool // same store state + args => same result
	// SideEffects lists anything beyond returning data, e.g.
	// "process_control", "approval_surface". Empty means none.
	SideEffects []string
	// QuickTool is "" when the function is not exposed to the main agent as
	// a quick.* tool, otherwise "quick.<name>" (an explicit, reviewed
	// opt-in — never inferred from QuickEligible alone).
	QuickTool string

	// Emits/EmitsOptional are proposer-only: the payload keys a proposer
	// always produces, and the ones it produces only when the connector's
	// schema declares them.
	Emits         []string
	EmitsOptional []string
}

// Learnable reports whether a handler may ever back a promoted (learned)
// intent: read-only, deterministic, free of side effects, of a class that
// is pure data work, and with no free-text argument (a learned intent must
// never need to interpret open-ended text).
func (f FunctionSpec) Learnable() bool {
	return f.ReadOnly && f.Deterministic && len(f.SideEffects) == 0 &&
		(f.Class == ClassLookup || f.Class == ClassCompute || f.Class == ClassFormat) &&
		!hasTextArg(f.Args)
}

// QuickEligible reports whether a handler may be declared as a quick.* tool
// for the main agent: read-only, no side effects, a plain lookup, and no
// free-text argument (the main agent should draft any free text itself).
func (f FunctionSpec) QuickEligible() bool {
	return f.ReadOnly && len(f.SideEffects) == 0 && f.Class == ClassLookup && !hasTextArg(f.Args)
}

func hasTextArg(args map[string]slots.Type) bool {
	for _, t := range args {
		if t == slots.TypeText {
			return true
		}
	}
	return false
}

// Functions is the full set of handler contracts LoadRegistry validates
// intents against: read intents target Read, write intents target Write.
type Functions struct {
	Read  map[string]FunctionSpec
	Write map[string]FunctionSpec
}

// SchemaInfo is a granted connector function's input schema, as the
// gateway reports it (from connectors.Registry) so a write intent's
// proposer can be checked against what the connector actually accepts.
type SchemaInfo struct {
	Required   []string
	Properties []string
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
