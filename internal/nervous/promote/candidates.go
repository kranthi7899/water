// Package promote implements the growth loop's read-only half: finding
// repeated main-agent quick-tool usage patterns that could later become a
// learned Tier 0 intent (Design §16). This package never writes an intent
// file, never touches the registry or the manifest, and never promotes
// anything — drafting and promotion are a later task (R-23).
package promote

import (
	"context"
	"crypto/sha256"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"water/internal/nervous"
	"water/internal/nervous/intents"
	"water/internal/nervous/reflex"
	"water/internal/nervous/tmpl"
	"water/internal/store"
)

// DefaultMinRepeats is `water route candidates`' default --min and GET
// /v1/route/candidates' default min= (Design §16: 5).
const DefaultMinRepeats = 5

// DefaultMaxRows bounds how many quick_only route_log rows Candidates scans
// per call, so DB I/O and unmarshal cost stay bounded even on a daemon with
// a very high turn volume over the lookback window, rather than scaling
// unboundedly with route_log's own size. Generous for any realistic single-
// user personal-assistant workload (500 turns/day for 30 days is 15,000);
// a caller scanning past this many quick_only rows in the window undercounts
// older ones, an acceptable degradation for a background detection
// heuristic, not a safety-relevant path.
const DefaultMaxRows = 20000

// MinCandidateDays is the fewest distinct local calendar days a qualifying
// signature group must span, regardless of minRepeats (Design §16: "at
// least 2 distinct local days" — a pattern that only ever happened in one
// sitting is not yet a habit).
const MinCandidateDays = 2

// maxSampleTurnIDs bounds how many turn ids one Candidate carries, enough
// for R-23's draft step ("up to 10 sample utterances from the group")
// without keeping every qualifying row in memory.
const maxSampleTurnIDs = 10

// Candidate is one repeated main-agent tool-call pattern that could become
// a learned Tier 0 intent. Detection only: nothing here judges whether the
// pattern SHOULD be promoted beyond the mechanical qualification rules.
type Candidate struct {
	// ID is the first 12 hex characters of sha256(Signature): stable across
	// runs, used as the handle water intent draft <candidate> (R-23) takes.
	ID string `json:"id"`
	// Signature is the canonical, sorted, deduplicated tool-call signature
	// (nervous.QuickOnlySignature's output), e.g.
	// "quick.calendar+quick.next_event".
	Signature string `json:"signature"`
	// Repeats is the count of distinct qualifying turns with this signature.
	Repeats int `json:"repeats"`
	// Days is the count of distinct local calendar days those turns span.
	Days int `json:"days"`
	// SampleTurnIDs are a few example turn ids (newest first, capped at
	// maxSampleTurnIDs) for R-23 to sample utterances from.
	SampleTurnIDs []string `json:"sample_turn_ids"`
}

// Candidates reads route_log (via s.QuickOnlyRoutes, which already filters
// to quick_only=1 rows at or after since) and groups the rows that qualify
// by tool signature, returning one Candidate per group that meets the
// repeat/day thresholds. It is read-only and always available — callers
// never need to check router.promotion.enabled to list candidates (Design
// §16 item 1); only drafting and promotion (R-23) are gated.
//
// sh is the twin's loaded intents.Shared (escalate words, clause joiners,
// skip words): candidate detection re-runs nervous.Eligible against each
// row's own utterance using it, so a reasoning/multi-clause turn can never
// become a candidate even if it was somehow marked quick_only. This is the
// one deliberate addition over the plan's originally sketched signature
// (which took no such parameter) — Eligible cannot be evaluated without
// the twin's actual shared config, and nothing else in this package's
// inputs carries it. maxRows <= 0 means DefaultMaxRows.
func Candidates(ctx context.Context, s *store.Store, sh intents.Shared, since time.Time, minRepeats, maxRows int) ([]Candidate, error) {
	if minRepeats <= 0 {
		minRepeats = DefaultMinRepeats
	}
	if maxRows <= 0 {
		maxRows = DefaultMaxRows
	}
	rows, err := s.QuickOnlyRoutes(ctx, since, maxRows)
	if err != nil {
		return nil, err
	}

	learnable := learnableQuickTools()
	skip := skipSet(sh.SkipWords)

	groups := map[string]*candidateGroup{}
	for _, row := range rows {
		if !qualifies(row, sh, skip, learnable) {
			continue
		}
		g := groups[row.ToolSignature]
		if g == nil {
			g = &candidateGroup{turns: map[string]bool{}, days: map[string]bool{}}
			groups[row.ToolSignature] = g
		}
		if g.turns[row.TurnID] {
			continue // route_log's turn_id is unique per row; defensive only
		}
		g.turns[row.TurnID] = true
		g.days[row.At.Local().Format("2006-01-02")] = true
		if len(g.samples) < maxSampleTurnIDs {
			g.samples = append(g.samples, row.TurnID)
		}
	}

	out := make([]Candidate, 0, len(groups))
	for sig, g := range groups {
		if len(g.turns) < minRepeats || len(g.days) < MinCandidateDays {
			continue
		}
		out = append(out, Candidate{
			ID:            candidateID(sig),
			Signature:     sig,
			Repeats:       len(g.turns),
			Days:          len(g.days),
			SampleTurnIDs: g.samples,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repeats != out[j].Repeats {
			return out[i].Repeats > out[j].Repeats
		}
		return out[i].Signature < out[j].Signature
	})
	return out, nil
}

// candidateGroup accumulates one tool-signature's qualifying rows before
// the repeat/day thresholds are checked.
type candidateGroup struct {
	turns   map[string]bool
	days    map[string]bool
	samples []string
}

// qualifies reports whether row may contribute to a candidate group
// (Design §16 item 1's row-level rule, applied literally).
func qualifies(row store.RouteRow, sh intents.Shared, skip map[string]bool, learnable map[string]bool) bool {
	if row.Owner != "main" || !row.QuickOnly || !row.ToolsAttributed {
		return false
	}
	if row.Outcome != "answered" || row.PossibleMiss {
		return false
	}
	if row.ToolSignature == "" {
		return false
	}
	u := tmpl.Normalize(row.Utterance, skip)
	if ok, _ := nervous.Eligible(u, sh); !ok {
		return false
	}
	for _, name := range strings.Split(row.ToolSignature, "+") {
		if !learnable[name] {
			return false
		}
	}
	return true
}

// learnableQuickTools maps every reflex.Table() handler's QuickTool id to
// whether its FunctionSpec.Learnable() (R-6) is true. A tool signature
// naming anything not in this map (an unknown or non-quick tool id) is
// treated as not learnable, the same as a handler that fails the check —
// candidate detection never assumes a name it doesn't recognize is safe.
func learnableQuickTools() map[string]bool {
	out := map[string]bool{}
	for _, h := range reflex.Table() {
		if h.Spec.QuickTool == "" {
			continue
		}
		out[h.Spec.QuickTool] = h.Spec.Learnable()
	}
	return out
}

// skipSet turns Shared.SkipWords into the set tmpl.Normalize needs. Shared
// only exposes a slice, and both internal/nervous (tier.go's wordSet) and
// internal/nervous/intents (shared.go's skipSet) already duplicate this
// same three-line loop rather than export it across a package boundary —
// this is the same deliberate, small duplication, not an oversight.
func skipSet(words []string) map[string]bool {
	if len(words) == 0 {
		return nil
	}
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

// candidateID is the first 12 hex characters of sha256(signature).
func candidateID(signature string) string {
	sum := sha256.Sum256([]byte(signature))
	return fmt.Sprintf("%x", sum)[:12]
}

// candidateIDPattern matches candidateID's own output shape exactly: 12
// lowercase hex characters, nothing else — no path separators, no ".",
// nothing that could escape a directory join.
var candidateIDPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

// ValidCandidateID reports whether id has the shape candidateID produces.
// Any caller that turns a client- or owner-supplied candidate id into a
// filesystem path (WritePending's name argument, or a
// PendingDir/<id>.yaml read, R-26's `water intent promote` and its daemon
// endpoint) must check this BEFORE the join: rejecting anything that isn't
// exactly 12 hex characters rules out path traversal (e.g.
// "../../../etc/passwd") by construction, rather than trying to blocklist
// "..".
func ValidCandidateID(id string) bool { return candidateIDPattern.MatchString(id) }
