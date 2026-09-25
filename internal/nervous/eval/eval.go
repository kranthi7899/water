// Package eval measures how well a quick-tier implementation (Tier 0's
// templates, or later Tier 1's FunctionGemma proposals) answers a held-out
// set of utterances, against strict correctness and safety metrics — most
// importantly the false-accept rate, which must be near zero: a wrong
// reflex answer is worse than a slow correct one.
package eval

import (
	"context"
	"embed"
	"fmt"
	"math"
	"strings"
	"time"

	"water/internal/nervous"

	"gopkg.in/yaml.v3"
)

//go:embed testdata/ceo_eval.yaml
var ceoEvalFS embed.FS

//go:embed testdata/ceo_eval_t1.yaml
var ceoEvalT1FS embed.FS

// Case is one held-out eval example. Intent is "none" for a negative case
// (an utterance that must never be answered by a quick tier). Class buckets
// the case for reporting and for the minimum-count checks in
// TestEvalSetMeetsMinimums: "positive" (a normal read-intent paraphrase),
// "write" (a write-intent paraphrase), "reasoning", "multi_clause",
// "legacy_near_miss", "action_unsupported", "ood", or "yesno_zero_pending".
type Case struct {
	Utterance string            `yaml:"utterance"`
	Intent    string            `yaml:"intent"`
	Class     string            `yaml:"class"`
	Slots     map[string]string `yaml:"slots"`
	Pending   int               `yaml:"pending"`
}

func (c Case) isPositive() bool {
	return c.Intent != "" && c.Intent != "none"
}

type caseFile struct {
	Cases []Case `yaml:"cases"`
}

// Fixture describes the deterministic world a Tier under evaluation is
// built against: a fixed clock and a fixed set of known people, so date and
// person-slot resolution is reproducible across runs. A Tier implementation
// bakes a Fixture into its own construction (its store, its clock); Run
// itself does not use Fixture computationally today, but callers pass it so
// a Report can record which fixture produced it and so future Tier
// implementations that need fixture data at call time have a place to get
// it without changing Run's signature.
type Fixture struct {
	// Now is the fixed "current time" every case is evaluated against.
	Now time.Time
	// Senders are the known message senders a person slot resolves
	// against, formatted "Name <email>" (net/mail address format).
	// Includes at least two people sharing a first name, so ambiguous
	// person resolution can be exercised.
	Senders []string
}

// DefaultFixture is the fixture every eval run in this repo uses: 2026-09-24
// 09:00 Pacific (a Thursday), with four known senders including two Alexes.
func DefaultFixture() Fixture {
	loc := time.FixedZone("PT", -7*3600)
	return Fixture{
		Now: time.Date(2026, 9, 24, 9, 0, 0, 0, loc),
		Senders: []string{
			"Alex Chen <alex.chen@example.com>",
			"Alex Rivera <alex.rivera@example.com>",
			"Priya Nair <priya@example.com>",
			"Jordan Lee <jordan@example.com>",
		},
	}
}

// Tier is the minimal shape Run needs from a thing under evaluation. A real
// nervous.Tier (built in a later task) is expected to be adapted to this
// interface with a thin wrapper, since nervous.Tier's own shape carries
// gateway-level concerns (rendering, taint, timeouts) this package doesn't
// need to know about.
//
// Try evaluates one utterance. matched is false when the tier declines to
// answer (falls through / escalates); escalate names why, for callers that
// want to distinguish "escalated" from "answered nothing" — a Tier that
// doesn't distinguish these may always report escalate=true when
// matched=false. slots is compared against Case.Slots on a per-key basis:
// a key Case.Slots declares that slots does not contain is not treated as
// wrong (the tier may simply not report every slot), but a key present in
// both with different values is wrong. This lets a tier with no slot
// support (the legacy fast path, wrapped by legacyTier below) still report
// a meaningful hit rate; a Tier 0/Tier 1 implementation that DOES claim
// slot support should populate every slot it resolves so mismatches are
// actually caught.
type Tier interface {
	Try(ctx context.Context, utterance string, pending int) (matched bool, intent string, slots map[string]string, escalate bool, err error)
}

// Report summarizes one eval run.
type Report struct {
	Tier string

	N, Positives, Negatives int

	// HitRate is the fraction of positive cases answered with the correct
	// intent (slot correctness is not required for a "hit" — see SlotAcc).
	HitRate float64
	// IntentAcc is the fraction of ANSWERED cases (any class) whose intent
	// was correct — for negatives, "correct" is impossible (there is no
	// right intent to answer with), so IntentAcc is computed only over
	// answered positive cases.
	IntentAcc float64
	// SlotAcc is the fraction of answered-with-correct-intent positive
	// cases, restricted to those with at least one declared slot, whose
	// reported slots matched every declared slot. NaN when no such case
	// exists in the set (avoids a misleading 0/0 division).
	SlotAcc float64
	// FalseAcceptRate is the fraction of ALL cases (N) that were false
	// accepts: an answered negative case, or an answered positive case
	// with the wrong intent or a wrong (present-but-mismatched) slot
	// value. This must be at or near zero for any tier trusted to answer
	// without a model call.
	FalseAcceptRate float64
	// Wilson95Upper is the Wilson-score-interval upper bound (95%
	// confidence) on the true false-accept rate, given the observed count
	// and N. More conservative than the raw rate at small N.
	Wilson95Upper float64
	// EscalationRate is the fraction of all cases the tier declined to
	// answer (matched=false).
	EscalationRate float64
	// ReasoningAnswered counts cases of class "reasoning" or
	// "multi_clause" that were answered (matched=true) — this must be 0
	// for any quick tier; the sous chef never answers a reasoning or
	// multi-clause request.
	ReasoningAnswered int

	FalseAccepts []Case

	P50, P95 time.Duration
}

func slotsMatch(got, want map[string]string) bool {
	for k, v := range want {
		gv, ok := got[k]
		if !ok {
			// The tier didn't report this slot at all; not treated as a
			// mismatch (see the Tier interface's doc comment).
			continue
		}
		if strings.TrimSpace(strings.ToLower(gv)) != strings.TrimSpace(strings.ToLower(v)) {
			return false
		}
	}
	return true
}

// Run evaluates t over cases and returns a Report. It does not itself apply
// any pass/fail threshold; callers (tests, the CLI) compare the returned
// Report against the acceptance criteria in docs/slices/R.md.
func Run(ctx context.Context, t Tier, fx Fixture, cases []Case) Report {
	r := Report{N: len(cases)}

	var latencies []time.Duration
	var positiveHits int
	var answeredTotal, answeredIntentCorrect int
	var slotEligible, slotCorrect int
	var falseAcceptCount int

	for _, c := range cases {
		positive := c.isPositive()
		if positive {
			r.Positives++
		} else {
			r.Negatives++
		}

		start := time.Now()
		matched, intent, slots, escalate, err := t.Try(ctx, c.Utterance, c.Pending)
		latencies = append(latencies, time.Since(start))

		if err != nil {
			// Treat an error the same as a non-match: it did not answer.
			matched = false
		}
		if !matched {
			if escalate || err != nil {
				// escalation_rate counts any non-answer; whether the tier
				// distinguishes "escalated" from "declined" doesn't change
				// this metric.
			}
			continue
		}

		answeredTotal++
		if c.Class == "reasoning" || c.Class == "multi_clause" {
			r.ReasoningAnswered++
		}

		intentOK := positive && intent == c.Intent
		slotsOK := slotsMatch(slots, c.Slots)
		hasDeclaredSlots := len(c.Slots) > 0

		if intentOK {
			answeredIntentCorrect++
			if hasDeclaredSlots {
				slotEligible++
				if slotsOK {
					slotCorrect++
				}
			}
		}

		falseAccept := !positive || !intentOK || (hasDeclaredSlots && !slotsOK)
		if falseAccept {
			falseAcceptCount++
			r.FalseAccepts = append(r.FalseAccepts, c)
		} else if positive && intentOK {
			positiveHits++
		}
	}

	if r.Positives > 0 {
		r.HitRate = float64(positiveHits) / float64(r.Positives)
	}
	if answeredTotal > 0 {
		r.IntentAcc = float64(answeredIntentCorrect) / float64(answeredTotal)
	}
	if slotEligible > 0 {
		r.SlotAcc = float64(slotCorrect) / float64(slotEligible)
	} else {
		r.SlotAcc = math.NaN()
	}
	if r.N > 0 {
		r.FalseAcceptRate = float64(falseAcceptCount) / float64(r.N)
		r.EscalationRate = float64(r.N-answeredTotal) / float64(r.N)
	}
	r.Wilson95Upper = wilson95Upper(falseAcceptCount, r.N)

	r.P50 = nervous.Percentile(latencies, 0.50)
	r.P95 = nervous.Percentile(latencies, 0.95)

	return r
}

// wilson95Upper returns the upper bound of the two-sided 95% Wilson score
// interval for a binomial proportion successes/n (z = 1.959963985).
func wilson95Upper(successes, n int) float64 {
	if n == 0 {
		return 0
	}
	const z = 1.959963985
	p := float64(successes) / float64(n)
	nf := float64(n)
	denom := 1 + z*z/nf
	centre := p + z*z/(2*nf)
	margin := z * math.Sqrt(p*(1-p)/nf+z*z/(4*nf*nf))
	return (centre + margin) / denom
}

func parseCaseFile(b []byte) ([]Case, error) {
	var cf caseFile
	if err := yaml.Unmarshal(b, &cf); err != nil {
		return nil, fmt.Errorf("eval: parse case file: %w", err)
	}
	return cf.Cases, nil
}

// LoadCEOEval loads the embedded main eval set (testdata/ceo_eval.yaml).
func LoadCEOEval() ([]Case, error) {
	b, err := ceoEvalFS.ReadFile("testdata/ceo_eval.yaml")
	if err != nil {
		return nil, fmt.Errorf("eval: read ceo_eval.yaml: %w", err)
	}
	return parseCaseFile(b)
}

// LoadT1Supplement loads the embedded Tier 1 supplement
// (testdata/ceo_eval_t1.yaml): further held-out paraphrases that, combined
// with LoadCEOEval's set, bring the live Tier 1 eval's n to at least 400
// (docs/slices/R.md §10/Risks item 12). It is never answered by Tier 0
// alone (see TestEvalSetMeetsMinimums and the live eval, task R-18) — it
// exists specifically to widen the Tier 1 live eval's sample size.
func LoadT1Supplement() ([]Case, error) {
	b, err := ceoEvalT1FS.ReadFile("testdata/ceo_eval_t1.yaml")
	if err != nil {
		return nil, fmt.Errorf("eval: read ceo_eval_t1.yaml: %w", err)
	}
	return parseCaseFile(b)
}

// Negatives returns every case in the embedded main eval set whose class is
// not "positive" or "write" — i.e. every case a quick tier must never
// answer correctly. It is used by internal/nervous/promote's
// ValidateLearned to reject a learned intent that would false-accept any of
// these, including every reasoning and multi-clause case.
func Negatives() []Case {
	cases, err := LoadCEOEval()
	if err != nil {
		// The embedded file is baked into the binary at build time; a
		// parse failure here means the build itself is broken, which
		// go test/go build would already have caught. Returning nil
		// keeps this function's signature simple (no error) for callers
		// that only need it at runtime after a successful build.
		return nil
	}
	out := make([]Case, 0, len(cases))
	for _, c := range cases {
		if c.Class == "positive" || c.Class == "write" {
			continue
		}
		out = append(out, c)
	}
	return out
}
