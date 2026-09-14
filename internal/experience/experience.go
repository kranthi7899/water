// Package experience is the offline, explicit growth and promotion-candidate
// review for a role's experience.md. It is a Go port of twin's
// soul_layer/experience_tool.py, adapted to water's format (uncited
// first-person prose in experience.md, sentence→source mapping in
// .index.json — twin keeps both in experience.md itself), not a
// reimplementation of its design:
//
//   - grow: feedback on a live exchange -> one reflection pass -> either a new
//     lesson appended to experience.md, or +1 independent-source support on an
//     existing lesson (a new .index.json entry against the same sentence), or
//     nothing. Every change is appended to results/experience_growth_log.jsonl.
//   - candidates: lists lessons whose support (distinct sourced entries for
//     that sentence) has reached a review threshold. Promotion into
//     frameworks.md stays a manual, reviewed edit — this package never writes
//     frameworks.md.
//
// Two rules carry over unchanged from twin: the agent never writes to
// experience.md while answering (this package is only ever invoked by the
// `water experience` command, never by agent.RunSingle or the orchestrator),
// and promotion is manual.
package experience

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"water/internal/backend"
	"water/internal/persona"
)

// GrowResult is the outcome of one reflection pass.
type GrowResult struct {
	Action    string `json:"action"`    // "new" | "reinforce" | "none"
	Matched   string `json:"matched"`   // existing lesson sentence, verbatim, when action=="reinforce"
	Lesson    string `json:"lesson"`    // new lesson sentence, when action=="new"
	Rationale string `json:"rationale"`
}

const reflectSystem = `You are reviewing feedback on a completed exchange to decide whether a role's
experience.md should grow. This is an offline review step, run only through an explicit command —
never while the role is answering a live prompt.

Respond with ONLY a single JSON object, no surrounding prose or code fences, matching this shape:
{"action": "new"|"reinforce"|"none", "matched": "", "lesson": "", "rationale": ""}

- Use "reinforce" only when the feedback confirms, in a genuinely independent situation, one of the
  lessons listed below. Set "matched" to that lesson's exact sentence, copied verbatim character for
  character, and leave "lesson" empty.
- Use "new" when the feedback teaches a transferable lesson not already captured. Write "lesson" as a
  single uncited, first-person sentence at the same abstraction level as the existing lessons below —
  no names, dates, or figures — and leave "matched" empty.
- Use "none" when the feedback is about style, is a one-off preference, or teaches nothing
  transferable. Leave "matched" and "lesson" empty.

Always fill "rationale" with one sentence explaining the classification.`

// Grow runs one reflection pass over feedback on a live exchange (question,
// the role's response, and reviewer feedback on it) and, unless dryRun,
// applies the result to agents/<role>/experience.md and .index.json under
// dir, then appends a record to agents/<role>/results/experience_growth_log.jsonl.
func Grow(ctx context.Context, b backend.Backend, dir, role, question, response, feedback string, dryRun bool) (GrowResult, error) {
	roleDir := filepath.Join(dir, role)
	lessons, err := paragraphs(roleDir)
	if err != nil {
		return GrowResult{}, err
	}

	var sb strings.Builder
	sb.WriteString("<existing_lessons>\n")
	for _, l := range lessons {
		sb.WriteString("- " + l + "\n")
	}
	sb.WriteString("</existing_lessons>\n\n")
	fmt.Fprintf(&sb, "<question>%s</question>\n<response>%s</response>\n<feedback>%s</feedback>\n",
		strings.TrimSpace(question), strings.TrimSpace(response), strings.TrimSpace(feedback))

	resp, err := b.Run(ctx, backend.Request{System: reflectSystem, Prompt: sb.String(), Role: role})
	if err != nil {
		return GrowResult{}, fmt.Errorf("reflection call: %w", err)
	}
	result, err := parseGrowResult(resp.Text)
	if err != nil {
		return GrowResult{}, fmt.Errorf("parsing reflection output: %w\nraw: %s", err, resp.Text)
	}

	stamp := time.Now().UTC().Format(time.RFC3339)
	if dryRun || result.Action == "none" {
		return result, nil
	}

	switch result.Action {
	case "new":
		if strings.TrimSpace(result.Lesson) == "" {
			return result, fmt.Errorf("action \"new\" returned an empty lesson")
		}
		if err := appendLesson(roleDir, role, result.Lesson, stamp); err != nil {
			return result, err
		}
	case "reinforce":
		matched := findLesson(lessons, result.Matched)
		if matched == "" {
			return result, fmt.Errorf("reinforce target not found verbatim among existing lessons: %q", result.Matched)
		}
		if err := reinforceLesson(roleDir, role, matched, stamp); err != nil {
			return result, err
		}
		result.Matched = matched
	default:
		return result, fmt.Errorf("unrecognised action %q", result.Action)
	}
	if err := appendGrowthLog(roleDir, stamp, result); err != nil {
		return result, err
	}
	return result, nil
}

// Candidate is a lesson whose independently-sourced support has reached the
// review threshold.
type Candidate struct {
	Sentence string
	Support  int
	Sources  []string
}

// Candidates lists lessons whose support — the number of distinct
// .index.json entries (by source_id) recorded against that exact sentence —
// is at least minSupport, most-supported first. It never writes
// frameworks.md; promotion into it stays a manual, reviewed edit.
func Candidates(dir, role string, minSupport int) ([]Candidate, error) {
	roleDir := filepath.Join(dir, role)
	fsys := os.DirFS(roleDir)
	idx, err := persona.LoadIndex(fsys, ".")
	if err != nil {
		return nil, err
	}
	bySentence := map[string]*Candidate{}
	var order []string
	for _, e := range idx.Entries {
		c, ok := bySentence[e.Sentence]
		if !ok {
			c = &Candidate{Sentence: e.Sentence}
			bySentence[e.Sentence] = c
			order = append(order, e.Sentence)
		}
		c.Support++
		c.Sources = append(c.Sources, e.SourceID)
	}
	var out []Candidate
	for _, s := range order {
		c := bySentence[s]
		if c.Support >= minSupport {
			out = append(out, *c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Support > out[j].Support })
	return out, nil
}

// paragraphs returns experience.md's body split into non-empty paragraphs —
// water's unit of "one lesson" since, unlike twin, water's experience.md
// carries no ### headers or IDs.
func paragraphs(roleDir string) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(roleDir, "experience.md"))
	if err != nil {
		return nil, err
	}
	doc, err := persona.ParseDoc(raw)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, p := range strings.Split(doc.Body, "\n\n") {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

// findLesson returns the existing lesson matching candidate verbatim (after
// whitespace trimming), or "" if none matches.
func findLesson(lessons []string, candidate string) string {
	candidate = strings.TrimSpace(candidate)
	for _, l := range lessons {
		if l == candidate {
			return l
		}
	}
	return ""
}

func appendLesson(roleDir, role, lesson, stamp string) error {
	p := filepath.Join(roleDir, "experience.md")
	raw, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	text := strings.TrimRight(string(raw), "\n") + "\n\n" + strings.TrimSpace(lesson) + "\n"
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		return err
	}
	return addIndexEntry(roleDir, role, persona.IndexEntry{
		Sentence:   strings.TrimSpace(lesson),
		SourceID:   "feedback-" + stamp,
		Source:     "reviewer feedback via `water experience " + role + " grow`, " + stamp,
		Confidence: 1.0,
	})
}

func reinforceLesson(roleDir, role, matched, stamp string) error {
	return addIndexEntry(roleDir, role, persona.IndexEntry{
		Sentence:   matched,
		SourceID:   "feedback-" + stamp,
		Source:     "reviewer feedback via `water experience " + role + " grow`, " + stamp,
		Confidence: 1.0,
	})
}

func addIndexEntry(roleDir, role string, entry persona.IndexEntry) error {
	fsys := os.DirFS(roleDir)
	idx, err := persona.LoadIndex(fsys, ".")
	if err != nil {
		return err
	}
	idx.Schema = 1
	idx.Role = role
	idx.Entries = append(idx.Entries, entry)
	b, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(roleDir, ".index.json"), append(b, '\n'), 0o644)
}

func appendGrowthLog(roleDir, stamp string, result GrowResult) error {
	dir := filepath.Join(roleDir, "results")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	line, err := json.Marshal(map[string]string{
		"at": stamp, "action": result.Action, "matched": result.Matched,
		"lesson": result.Lesson, "rationale": result.Rationale,
	})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "experience_growth_log.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(line, '\n'))
	return err
}

// parseGrowResult extracts the JSON object from a backend response, tolerant
// of a surrounding ```json fence or stray prose around it.
func parseGrowResult(text string) (GrowResult, error) {
	text = strings.TrimSpace(text)
	if i, j := strings.IndexByte(text, '{'), strings.LastIndexByte(text, '}'); i >= 0 && j > i {
		text = text[i : j+1]
	}
	var r GrowResult
	if err := json.Unmarshal([]byte(text), &r); err != nil {
		return GrowResult{}, err
	}
	switch r.Action {
	case "new", "reinforce", "none":
	default:
		return GrowResult{}, fmt.Errorf("unrecognised action %q", r.Action)
	}
	return r, nil
}
