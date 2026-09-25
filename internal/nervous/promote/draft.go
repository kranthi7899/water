package promote

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"water/internal/backend"
	"water/internal/nervous/intents"
	"water/internal/nervous/reflex"
)

// DraftRole tags every Draft call's backend.Request.Role, purely for
// tracing/attribution — backend.go's own doc comment is explicit that Role
// "must never affect which backend is used".
const DraftRole = "promote.draft"

// draftSystemPrompt describes the task and the exact shape a reply must
// have. It is deliberately a short summary of the template grammar (Design
// §16 item 2: "a short static description is fine"), not the full grammar
// spec — the model only needs enough to write a handful of alternatives and
// optional-word groups, not to reimplement the parser.
const draftSystemPrompt = `You write one Water "sous chef" intent file: a small YAML document
describing a deterministic phrasing pattern that always answers the same
way, by calling one function that already exists in Water's registry. You
never invent a new capability — only a new PHRASING of a function's
existing arguments.

Water's template grammar, briefly:
  - plain words must appear literally (matching is case- and
    punctuation-insensitive).
  - (a|b|c) matches exactly one of the alternatives.
  - [optional words] may be present or absent.
  - {slot_name} captures one value at that position. Every slot used in a
    template must also appear in the intent's own slots: section, with a
    type matching the target function's argument.
  - Do not use <rule_name> references; write the words out directly.

Reply with exactly one YAML document and nothing else: no markdown code
fences, no commentary before or after it. Match this shape:

id: learned.placeholder
description: <one short line>
kind: read
function: <the exact target function id given below>
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
slots:
  <slot_name>: {type: <slot type>, required: <true|false>}
templates:
  - "<template 1>"
  - "<template 2, a different phrasing of the same request>"
examples:
  - "<a natural paraphrase, for declaration only>"
tests:
  - {utterance: "<an utterance one of your templates matches>", intent: learned.placeholder, slots: {<slot_name>: <expected label>}}
  - {utterance: "<an unrelated utterance>", intent: "none"}

Include slots: only if the function takes arguments. id, origin and
provenance are overwritten by Water itself after you reply, regardless of
what you write for them — write a reasonable placeholder id if you like.`

// fencedBlockRe strips a single ``` or ```yaml/```yml fenced block, in case
// the model wraps its reply in one despite being asked not to.
var fencedBlockRe = regexp.MustCompile("(?s)```(?:ya?ml)?\\s*\\n?(.*?)\\n?```")

// Draft makes exactly one cold backend.Run call — be is a plain
// backend.Backend, which has no notion of a warm session at all, so the
// warm chat session can never be touched by construction, not just by
// convention — asking the model to propose an intent file for cand's
// repeated quick-tool phrasing pattern (Design §16 item 2). The reply's
// id/origin/provenance are always overwritten with code-computed values
// afterward, regardless of what the model wrote for them: a promoted
// intent's identity and history must never be something the model itself
// gets to claim.
func Draft(ctx context.Context, be backend.Backend, model string, spec intents.FunctionSpec, cand Candidate, sampleUtterances []string) ([]byte, error) {
	if be == nil {
		return nil, fmt.Errorf("promote: draft: a backend is required")
	}
	resp, err := be.Run(ctx, backend.Request{
		System: draftSystemPrompt,
		Prompt: buildDraftPrompt(spec, cand, sampleUtterances),
		Role:   DraftRole,
		Model:  model,
	})
	if err != nil {
		return nil, fmt.Errorf("promote: draft: model call: %w", err)
	}

	yamlText := stripFencing(resp.Text)
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(yamlText), &doc); err != nil {
		return nil, fmt.Errorf("promote: draft: model output did not parse as YAML: %w", err)
	}
	if doc == nil {
		doc = map[string]any{}
	}

	// Code, never the model, decides identity and provenance.
	newID := "learned." + sanitizeName(cand.Signature, cand.ID)
	// Every positive tests: entry must reference THIS file's own id, but
	// the model cannot know newID in advance (it's derived from the
	// candidate's signature and id, computed here, after the model reply
	// already exists) — the system prompt asks it to write a placeholder
	// id consistently instead, which is rewritten to the real one now. A
	// "none" entry (a negative test) is left alone.
	rewriteTestIntents(doc, newID)
	doc["id"] = newID
	doc["origin"] = "learned"
	doc["provenance"] = map[string]any{
		"candidate":   cand.ID,
		"turn_ids":    cand.SampleTurnIDs,
		"repeats":     cand.Repeats,
		"drafted_at":  time.Now().UTC(),
		"draft_model": model,
		// registry_hash and promoted_at are left unset here: they describe
		// the registry state at PROMOTION time (Design §16 item 4), which
		// this drafting step has no registry to compute against yet.
	}

	out, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("promote: draft: re-encode: %w", err)
	}
	return out, nil
}

// rewriteTestIntents walks doc["tests"] (a list of {utterance, intent, ...}
// maps, as yaml.Unmarshal decodes it into map[string]any) and rewrites
// every entry whose intent is set and not "none" to newID — a learned
// intent file only ever declares itself, so any positive test entry must,
// by definition, reference this same file's own (code-assigned) id.
// Malformed shapes (not a list, not a map, no intent key) are left alone;
// the base registry validation that runs after Draft returns will reject
// the file on its own terms if the model's reply was structurally invalid.
func rewriteTestIntents(doc map[string]any, newID string) {
	tests, ok := doc["tests"].([]any)
	if !ok {
		return
	}
	for _, raw := range tests {
		tc, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		intentVal, ok := tc["intent"].(string)
		if !ok || intentVal == "none" {
			continue
		}
		tc["intent"] = newID
	}
}

// ExtractID reads just the id: field back out of a drafted or promoted
// intent file, for a caller (the gateway's draft endpoint) that wants to
// report it without re-parsing the whole document into an intents.Intent.
// "" on any parse failure.
func ExtractID(fileBytes []byte) string {
	var doc struct {
		ID string `yaml:"id"`
	}
	if err := yaml.Unmarshal(fileBytes, &doc); err != nil {
		return ""
	}
	return doc.ID
}

// stripFencing removes a single leading/trailing ``` fenced block, if the
// model wrapped its reply in one despite the system prompt asking it not
// to. Text with no fencing at all is returned trimmed, unchanged.
func stripFencing(s string) string {
	s = strings.TrimSpace(s)
	if m := fencedBlockRe.FindStringSubmatch(s); len(m) == 2 {
		return strings.TrimSpace(m[1])
	}
	return s
}

var nonIDRunRe = regexp.MustCompile(`[^a-z0-9_]+`)

// sanitizeName derives a lowercase_snake_case fragment for learned.<name>
// from a candidate's tool signature (e.g. "quick.latest_mail" ->
// "latest_mail"), with the candidate id's first 6 hex characters appended so
// two different signatures that happen to sanitize to the same words can
// never collide with each other on disk.
func sanitizeName(signature, candidateID string) string {
	s := strings.ToLower(signature)
	s = strings.ReplaceAll(s, "quick.", "")
	s = nonIDRunRe.ReplaceAllString(s, "_")
	s = strings.Trim(s, "_")
	if s == "" {
		s = "candidate"
	}
	if len(s) > 40 {
		s = strings.Trim(s[:40], "_")
	}
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		s = "c_" + s
	}
	suffix := candidateID
	if len(suffix) > 6 {
		suffix = suffix[:6]
	}
	return s + "_" + suffix
}

// SpecForQuickTool reverse-looks-up a quick.* tool name (as it appears in a
// route_log tool_signature, e.g. "quick.latest_mail") to the reflex handler
// that exposes it, returning its FunctionSpec (spec.ID is the real function
// id, e.g. "store.latest_messages", for the draft prompt and the drafted
// intent's own function: field). ok is false for a name reflex.Table()
// doesn't recognize as any handler's QuickTool.
func SpecForQuickTool(name string) (spec intents.FunctionSpec, ok bool) {
	for _, h := range reflex.Table() {
		if h.Spec.QuickTool == name {
			return h.Spec, true
		}
	}
	return intents.FunctionSpec{}, false
}

func buildDraftPrompt(spec intents.FunctionSpec, cand Candidate, samples []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Target function id: %s\n", spec.ID)
	fmt.Fprintf(&b, "Function class: %s\n", spec.Class)
	required := make(map[string]bool, len(spec.Required))
	for _, r := range spec.Required {
		required[r] = true
	}
	if len(spec.Args) == 0 {
		fmt.Fprintln(&b, "Arguments: none")
	} else {
		fmt.Fprintln(&b, "Arguments (name: type, required):")
		for name, typ := range spec.Args {
			fmt.Fprintf(&b, "  - %s: %s (required=%v)\n", name, typ, required[name])
		}
	}
	fmt.Fprintf(&b, "\nThis exact tool-call pattern was used %d times across %d distinct days by the main agent, always answered cleanly.\n", cand.Repeats, cand.Days)
	if n := len(samples); n > 0 {
		if n > 10 {
			n = 10
		}
		fmt.Fprintln(&b, "\nSample utterances this new intent should match:")
		for _, u := range samples[:n] {
			fmt.Fprintf(&b, "  - %q\n", u)
		}
	}
	return b.String()
}
