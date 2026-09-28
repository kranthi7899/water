package intents

import (
	"fmt"
	"regexp"
	"strings"

	"water/internal/twins"
)

// actionRe is the shape of a manifest function id, "connector.function".
var actionRe = regexp.MustCompile(`^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`)

// plannedActions mirrors internal/decisions/registry.go's plannedActions:
// write functions the evolution plan names for the next increment but no
// manifest grants yet. TestPlannedActionsMatchDecisions parses that file
// with go/parser and requires identical keys, so the two lists can't drift
// without this slice ever needing to import or edit internal/decisions.
//
// A write intent may name one of these before the manifest grants it: it
// loads and validates, but sits Active==false until the manifest catches
// up (checkAction below). Anything else must already be a level-A function,
// so a misspelling fails the whole load instead of an intent silently never
// activating.
var plannedActions = map[string]bool{
	"gmail.draft_message": true,
	"gmail.send_message":  true,
	"gcal.create_event":   true,
	"gcal.move_event":     true,
}

// checkAction resolves one write intent's action id against the manifest.
// It returns (active, requiresApproval, inactiveReason, err): err is a
// load-time error (the action id is malformed, its connector isn't
// declared, it is granted at a level other than A or D, or it names neither
// a granted nor a planned action); inactiveReason is set only when the
// action is a recognized planned one not yet granted, in which case the
// intent loads successfully but is excluded from Candidates() until a later
// registry load finds it granted.
//
// requiresApproval is only meaningful when active is true: a level-A action
// needs an approved envelope (the sous chef queues one via ActionSink and
// nothing executes until the CEO decides it), while a level-D action is
// "nothing leaves" by construction (internal/gate.NeedsEnvelope never
// requires an envelope for D), so its proposal IS the answer -- delivered
// directly, with no envelope and no gate call at all (docs/slices/R.md
// Risk item 24, resolved by this task).
func checkAction(a string, m *twins.Manifest, schema func(string) (SchemaInfo, bool), p FunctionSpec) (active bool, requiresApproval bool, inactiveReason string, err error) {
	if !actionRe.MatchString(a) {
		return false, false, "", fmt.Errorf("%q is not a connector.function id", a)
	}
	connector, _, _ := strings.Cut(a, ".")
	declared := false
	for _, c := range m.Connectors {
		if c.Name == connector {
			declared = true
			break
		}
	}
	if !declared {
		return false, false, "", fmt.Errorf("%s: connector %s is not in the %s manifest", a, connector, m.ID)
	}
	if f, ok := m.Function(a); ok {
		if f.Level != twins.A && f.Level != twins.D {
			return false, false, "", fmt.Errorf("%s is level %s; write intents must target a level-A or level-D function", a, f.Level)
		}
		if schema != nil {
			si, ok := schema(a)
			if !ok {
				return false, false, "", fmt.Errorf("%s: no schema available for a granted function", a)
			}
			props := map[string]bool{}
			for _, k := range si.Properties {
				props[k] = true
			}
			for _, k := range si.Required {
				if !containsString(p.Emits, k) {
					return false, false, "", fmt.Errorf("%s: proposer does not emit required schema key %q", a, k)
				}
			}
			for _, k := range p.Emits {
				if !props[k] {
					return false, false, "", fmt.Errorf("%s: proposer emits %q, which the schema does not declare", a, k)
				}
			}
			for _, k := range p.EmitsOptional {
				if !props[k] {
					return false, false, "", fmt.Errorf("%s: proposer's optional emit %q is not in the schema", a, k)
				}
			}
		}
		return true, f.Level == twins.A, "", nil
	}
	if plannedActions[a] {
		return false, false, fmt.Sprintf("%s is planned but not granted in the %s manifest", a, m.ID), nil
	}
	return false, false, "", fmt.Errorf("%s is neither a function in the %s manifest nor a planned action", a, m.ID)
}
