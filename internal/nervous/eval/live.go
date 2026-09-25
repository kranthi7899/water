package eval

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/mail"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"water/internal/approvals"
	"water/internal/nervous"
	"water/internal/nervous/intents"
	"water/internal/nervous/reflex"
	"water/internal/nervous/sidecar"
	"water/internal/nervous/slots"
	"water/internal/nervous/t1"
	"water/internal/nervous/tmpl"
	"water/internal/store"
)

// Tier1LiveOptions configures one live Tier 1 eval run (RunTier1Live):
// against a real llama-server subprocess serving the actually-downloaded
// FunctionGemma model, over the combined ceo_eval.yaml + ceo_eval_t1.yaml
// case sets, driven through the real cascade (eligibility, then Tier 0,
// then Tier 1 only on a clean "no_match") exactly as production runs it.
type Tier1LiveOptions struct {
	// Home is $WATER_HOME. The sidecar's working directory is Home, and the
	// resulting eval-gate record is written to
	// Home/router/tier1_eval.json (sidecar.WriteEvalRecord).
	Home string
	// LlamaBin is the llama-server executable; PATH-resolved by os/exec.
	// Defaults to "llama-server" when empty.
	LlamaBin string
	// ModelPath is the GGUF file to serve.
	ModelPath string
	// Registry supplies the tool declarations Tier 1 offers the model and
	// the registry hash the written eval record is stamped with. Required.
	Registry *intents.Registry
	// Fixture is the deterministic world every case is evaluated against
	// (fixed clock, known message senders). Defaults to DefaultFixture()
	// when its Now is zero.
	Fixture Fixture
	// StartupTimeout bounds how long RunTier1Live waits for the sidecar to
	// report healthy before giving up. Defaults to 60s when zero.
	StartupTimeout time.Duration
}

// RunTier1Live is the mechanism both `water route eval --tier1`
// (internal/cli/cmd_route.go) and TestTier1Live drive. It does not itself
// compare the result against the gate thresholds — sidecar.EvalRecord.Check
// does that; callers decide what to do with a failing record. The returned
// Report is the same detailed breakdown Run always produces, for a caller
// that wants to print more than the four numbers EvalRecord keeps.
//
// Known limitation (documented rather than silently glossed over): the Tier
// under evaluation here reports only which intent answered a case, not its
// resolved slot values (TryTier0/TryTier1 return a rendered render.Result,
// which carries a human-phrased interpretation, not the canonical slot
// Labels a Case.Slots comparison needs, and plumbing those through render.Result
// is out of this task's scope). The live eval's false-accept rate therefore
// catches a wrong intent or an answered negative/reasoning case, exactly
// the dominant safety risk the gate exists for, but not a right-intent
// wrong-slot-value answer. The offline, fake-client tests in
// internal/nervous/tier1_test.go exercise slot-level grounding directly.
func RunTier1Live(ctx context.Context, opts Tier1LiveOptions) (sidecar.EvalRecord, Report, error) {
	if opts.Registry == nil {
		return sidecar.EvalRecord{}, Report{}, fmt.Errorf("eval: RunTier1Live: Registry is required")
	}
	fixture := opts.Fixture
	if fixture.Now.IsZero() {
		fixture = DefaultFixture()
	}
	startupTimeout := opts.StartupTimeout
	if startupTimeout <= 0 {
		startupTimeout = 60 * time.Second
	}
	bin := opts.LlamaBin
	if bin == "" {
		bin = "llama-server"
	}

	modelSHA256, err := sha256File(opts.ModelPath)
	if err != nil {
		return sidecar.EvalRecord{}, Report{}, fmt.Errorf("eval: hash model file: %w", err)
	}

	sup := sidecar.New(sidecar.Config{Bin: bin, ModelPath: opts.ModelPath, Home: opts.Home})
	if err := sup.Start(ctx); err != nil {
		return sidecar.EvalRecord{}, Report{}, fmt.Errorf("eval: start sidecar: %w", err)
	}
	defer sup.Stop()

	if err := waitHealthy(ctx, sup, startupTimeout); err != nil {
		return sidecar.EvalRecord{}, Report{}, err
	}

	client, err := t1.NewHTTP(sup.Endpoint())
	if err != nil {
		return sidecar.EvalRecord{}, Report{}, fmt.Errorf("eval: build t1 client: %w", err)
	}

	st, cleanup, err := fixtureStore()
	if err != nil {
		return sidecar.EvalRecord{}, Report{}, err
	}
	defer cleanup()

	tr := &tier1EvalTier{
		reg:    opts.Registry,
		client: client,
		deps: reflex.Deps{
			Store:     reflex.NewStoreView(st),
			Approvals: fixturePendingLister{},
			Now:       func() time.Time { return fixture.Now },
		},
		ents: fixtureEntities(fixture),
		now:  fixture.Now,
	}

	cases, err := combinedTier1Cases()
	if err != nil {
		return sidecar.EvalRecord{}, Report{}, err
	}

	// One throwaway warm-up call: the model is already resident once
	// llama-server reports healthy, but the very first real request can
	// still carry one-time setup cost (e.g. KV cache allocation) that would
	// otherwise bias the warm p95 measurement on a case count this small.
	_, _, _, _, _ = tr.Try(ctx, "warm up the sidecar please", 0)
	tr.resetLatencies()

	report := Run(ctx, tr, fixture, cases)

	rec := sidecar.EvalRecord{
		ModelSHA256:   modelSHA256,
		RegistryHash:  opts.Registry.Hash(),
		N:             report.N,
		FalseAccepts:  len(report.FalseAccepts),
		FARate:        report.FalseAcceptRate,
		Wilson95Upper: report.Wilson95Upper,
		WarmP95Ms:     int(tr.warmP95().Milliseconds()),
		At:            time.Now(),
	}
	if err := sidecar.WriteEvalRecord(opts.Home, rec); err != nil {
		return rec, report, fmt.Errorf("eval: write eval record: %w", err)
	}
	return rec, report, nil
}

// combinedTier1Cases loads LoadCEOEval() plus LoadT1Supplement(), the >=400
// case combined set the live Tier 1 eval requires (docs/slices/R.md §10).
func combinedTier1Cases() ([]Case, error) {
	main, err := LoadCEOEval()
	if err != nil {
		return nil, err
	}
	supp, err := LoadT1Supplement()
	if err != nil {
		return nil, err
	}
	return append(append([]Case{}, main...), supp...), nil
}

// tier1EvalTier adapts the real cascade (eligibility, then Tier 0, then
// Tier 1 only on a clean "no_match") to the Tier interface Run needs —
// exactly the routing decision nervous.Handle makes, minus the turn state
// machine, rendering and route-log plumbing this package doesn't need.
// Running the full cascade (not Tier 1 in isolation) matters: it is the
// only way to reproduce production's guarantee that Tier 1 never even sees
// a reasoning/multi-clause/escalate-word utterance, and ceo_eval.yaml's
// positive cases are deliberately held out from Tier 0's own template
// phrasings, so a meaningful share of them really do reach Tier 1 in
// practice.
type tier1EvalTier struct {
	reg    *intents.Registry
	client t1.Client
	deps   reflex.Deps
	ents   slots.Entities
	now    time.Time

	mu          sync.Mutex
	t1Latencies []time.Duration
}

func (e *tier1EvalTier) resetLatencies() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.t1Latencies = nil
}

// warmP95 returns the 95th-percentile latency across only the calls that
// actually reached Tier 1's sidecar — not the full cascade's Report.P95,
// which would be diluted by every case Tier 0 answered (or eligibility
// rejected) without ever making a request, understating Tier 1's own
// warm-inference latency relative to the gate's warm_p95_ms threshold.
func (e *tier1EvalTier) warmP95() time.Duration {
	e.mu.Lock()
	sorted := append([]time.Duration{}, e.t1Latencies...)
	e.mu.Unlock()
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	return percentile(sorted, 0.95)
}

func liveWordSet(words []string) map[string]bool {
	if len(words) == 0 {
		return nil
	}
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

func (e *tier1EvalTier) Try(ctx context.Context, utterance string, pending int) (matched bool, intent string, slotsOut map[string]string, escalate bool, err error) {
	u := tmpl.Normalize(utterance, liveWordSet(e.reg.Shared().SkipWords))

	if ok, _ := nervous.Eligible(u, e.reg.Shared()); !ok {
		return false, "", nil, true, nil
	}

	res, reason, tErr := nervous.TryTier0(ctx, e.reg, e.deps, u, pending, e.now, e.ents)
	if tErr != nil {
		return false, "", nil, true, nil
	}
	if res != nil {
		return true, res.Intent, nil, false, nil
	}
	if reason != "no_match" {
		return false, "", nil, true, nil
	}

	start := time.Now()
	res, _, tErr = nervous.TryTier1(ctx, e.reg, e.deps, e.client, u, e.now, e.ents)
	d := time.Since(start)
	e.mu.Lock()
	e.t1Latencies = append(e.t1Latencies, d)
	e.mu.Unlock()
	if tErr != nil {
		return false, "", nil, true, tErr
	}
	if res == nil {
		return false, "", nil, true, nil
	}
	return true, res.Intent, nil, false, nil
}

func fixtureEntities(fx Fixture) slots.Entities {
	var people []slots.Person
	for _, s := range fx.Senders {
		addr, err := mail.ParseAddress(s)
		if err != nil {
			continue
		}
		people = append(people, slots.Person{Name: addr.Name, Email: addr.Address})
	}
	return slots.Entities{People: people}
}

type fixturePendingLister struct{}

func (fixturePendingLister) Pending(ctx context.Context) ([]approvals.Envelope, error) {
	return nil, nil
}

func waitHealthy(ctx context.Context, sup *sidecar.Supervisor, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if sup.Healthy(ctx) {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("eval: sidecar did not become healthy within %s", timeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fixtureStore opens a fresh, empty, temp-dir-backed store for the live
// eval's reflex handlers to read from. Never the owner's real ~/.water
// store: it starts empty (no events, no messages) on every run, which is
// sufficient because Run's Tier interface only compares matched
// intent/slots against each Case, never a handler's rendered content.
func fixtureStore() (*store.Store, func(), error) {
	dir, err := os.MkdirTemp("", "water-tier1-eval-*")
	if err != nil {
		return nil, nil, fmt.Errorf("eval: temp dir: %w", err)
	}
	st, err := store.Open(filepath.Join(dir, "eval.db"))
	if err != nil {
		os.RemoveAll(dir)
		return nil, nil, fmt.Errorf("eval: open fixture store: %w", err)
	}
	cleanup := func() {
		st.Close()
		os.RemoveAll(dir)
	}
	return st, cleanup, nil
}
