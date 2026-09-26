package nervous

import (
	"time"

	"water/internal/nervous/intents"
	"water/internal/nervous/reflex"
	"water/internal/nervous/slots"
	"water/internal/nervous/tmpl"
)

// Proposal is a candidate answer any quick tier can offer: which intent it
// thinks matched, and the slot spans it captured. Tier 0 builds Captures
// from a template match; Validate is the one place a tier's output is
// checked.
type Proposal struct {
	Intent   string
	Captures []tmpl.Capture
}

// Validated is a Proposal that passed every check: a real, active,
// enabled intent with every slot resolved (or its declared default
// applied), ready to hand to the intent's handler.
type Validated struct {
	Intent intents.Intent
	Args   reflex.Args
	Labels map[string]string // slot name -> Value.Label, the canonical form
	Spoken map[string]string // slot name -> Value.Spoken, the header echo form
}

// Validate is the one validator every quick tier's proposal goes through
// (Design §11.3): the intent must be a live candidate, every captured slot
// must resolve, and every required slot must resolve either from a capture
// or the intent's own default. now anchors relative dates/times; ents
// supplies the known people (and, later, projects) person/project slots
// resolve against.
func Validate(reg *intents.Registry, p Proposal, now time.Time, ents slots.Entities) (Validated, string, bool) {
	it, ok := candidateByID(reg, p.Intent)
	if !ok {
		return Validated{}, "no_match", false
	}

	captured := make(map[string]tmpl.Capture, len(p.Captures))
	for _, c := range p.Captures {
		captured[c.Slot] = c
	}
	for name := range captured {
		if _, declared := it.Slots[name]; !declared {
			// A template only ever compiles against its intent's own
			// declared slots (LoadRegistry enforces this), so a capture
			// naming an undeclared slot here would mean caller/registry
			// mismatch, not a bad utterance. Fail closed rather than
			// silently drop it.
			return Validated{}, "slot_unresolved", false
		}
	}

	args := make(reflex.Args, len(it.Slots))
	labels := make(map[string]string, len(it.Slots))
	spoken := make(map[string]string, len(it.Slots))

	for name, spec := range it.Slots {
		spotSpec := slots.Spec{Min: spec.Min, Max: spec.Max}

		if c, has := captured[name]; has {
			v, outcome, _ := slots.Resolve(spec.Type, c, spotSpec, now, ents)
			if outcome != slots.Resolved {
				return Validated{}, "slot_unresolved", false
			}
			args[name], labels[name], spoken[name] = v, v.Label, v.Spoken
			continue
		}

		if spec.Default != "" {
			v, outcome, _ := slots.ResolveString(spec.Type, spec.Default, spotSpec, now, ents)
			if outcome != slots.Resolved {
				return Validated{}, "slot_unresolved", false
			}
			args[name], labels[name], spoken[name] = v, v.Label, v.Spoken
			continue
		}

		if spec.Required {
			return Validated{}, "slot_unresolved", false
		}
		// Optional, uncaptured, no default: left absent from args. A
		// handler that cares checks via the map's comma-ok form.
	}

	return Validated{Intent: it, Args: args, Labels: labels, Spoken: spoken}, "", true
}

func candidateByID(reg *intents.Registry, id string) (intents.Intent, bool) {
	for _, it := range reg.Candidates() {
		if it.ID == id {
			return it, true
		}
	}
	return intents.Intent{}, false
}
