package decisions

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"water"
	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/connectors"
	"water/internal/connectors/fake"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

const testManifest = `id: t
name: T
usage: {window: 1h, model_calls: 50}
connectors:
  - name: gmail
    functions:
      - {name: list_messages, level: R}
      - {name: get_message, level: R}
      - {name: send_email, level: A}
  - name: gdrive
    functions:
      - {name: search_files, level: R}
      - {name: read_file, level: R}
`

func manifest(t *testing.T, src string) *twins.Manifest {
	t.Helper()
	m, err := twins.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func init() {
	RegisterCompute("internal://test.sum", func(_ context.Context, in ComputeInput) (ComputeResult, error) {
		n := 0
		for _, r := range in.Resolved {
			n += len(r.Records)
		}
		if n == 0 {
			return ComputeResult{}, nil
		}
		return ComputeResult{Figures: map[string]Figure{"related_count": {Value: n, Source: "code:test.sum"}}}, nil
	})
	RegisterCompute("test.fail", func(context.Context, ComputeInput) (ComputeResult, error) {
		return ComputeResult{}, errors.New("spreadsheet unreadable")
	})
	RegisterCompute("test.panic", func(context.Context, ComputeInput) (ComputeResult, error) { panic("boom") })
	RegisterCompute("test.unsourced", func(context.Context, ComputeInput) (ComputeResult, error) {
		return ComputeResult{Figures: map[string]Figure{
			"runway_months": {Value: 7.5, Source: "code:runway"},
			"invented":      {Value: 4200},
			"forged":        {Value: 9, Source: "gmail:never-fetched"},
		}}, nil
	})
}

const budgetYAML = `id: budget
title: Budget request
trigger: someone is asking for money
needs:
  - name: requester_history
    fetch: gmail.list_messages
    kind: lookup
    args: {query: "from:{sender}"}
  - name: related_count
    fetch: internal://test.sum
    kind: computation
default_rule: Approve under $2,000.
staged_actions: [gmail.send_email, gmail.draft_message]
severity_weight: 2
---
Notes for maintainers: the threshold follows the board policy.
`

const investorYAML = `id: investor
title: Investor request
trigger: an investor wants something
needs:
  - {name: docs, fetch: gdrive.search_files, kind: research, args: {query: "{keywords}"}}
default_rule: Route to the CEO.
severity_weight: 3
`

func registry(t *testing.T, files map[string]string) *Registry {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys["twins/t/decisions/"+name] = &fstest.MapFile{Data: []byte(body)}
	}
	r, err := LoadRegistry(fsys, manifest(t, testManifest))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// fakeGate answers each function from a table; a missing entry is a gate
// denial.
type fakeGate struct {
	answers map[string]func(gate.Call) (gate.Result, error)
	calls   []gate.Call
}

func (g *fakeGate) Invoke(_ context.Context, c gate.Call) (gate.Result, error) {
	g.calls = append(g.calls, c)
	if f, ok := g.answers[c.Function]; ok {
		return f(c)
	}
	return gate.Result{}, &gate.DenyError{Reason: c.Function + " is not in the manifest"}
}

func found(recs ...store.Record) func(gate.Call) (gate.Result, error) {
	return func(gate.Call) (gate.Result, error) { return gate.Result{Records: recs, Untrusted: true}, nil }
}

func none(gate.Call) (gate.Result, error) { return gate.Result{}, nil }

func msg(id string, ext bool, from, subject string) *store.Message {
	return &store.Message{Meta: store.Meta{Source: "gmail", SourceID: id, External: ext}, From: from, Subject: subject, Body: "Can you approve this?"}
}

func TestLoadRegistryWellFormed(t *testing.T) {
	r := registry(t, map[string]string{"budget.yaml": budgetYAML, "investor.yaml": investorYAML, "README.md": "ignored"})
	idx := r.Index()
	if len(idx) != 2 || idx[0] != (IndexEntry{"budget", "someone is asking for money"}) || idx[1] != (IndexEntry{"investor", "an investor wants something"}) {
		t.Fatalf("index: %+v", idx)
	}
	b, ok := r.Lookup("budget")
	if !ok || len(b.Needs) != 2 || b.SeverityWeight != 2 || !b.Needs[1].Internal() || b.Needs[0].Args["query"] != "from:{sender}" {
		t.Fatalf("budget: %+v", b)
	}
	if b.Notes != "Notes for maintainers: the threshold follows the board policy." {
		t.Fatalf("notes: %q", b.Notes)
	}
	g, ok := r.Lookup(GenericID)
	if !ok || g.ID != GenericID || g.DefaultRule != "" || len(g.StagedActions) != 0 || len(g.Needs) != 2 {
		t.Fatalf("generic: %+v", g)
	}
	if r.Generic().ID != GenericID {
		t.Fatal("Generic() wrong")
	}
}

func TestLoadRegistryEmptyDirIsOnlyGeneric(t *testing.T) {
	r, err := LoadRegistry(fstest.MapFS{}, manifest(t, testManifest))
	if err != nil || len(r.Index()) != 0 || r.Generic().ID != GenericID {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestEmbeddedCEORegistryLoads(t *testing.T) {
	m, err := twins.Load(water.TwinsFS(), "ceo")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadRegistry(water.TwinsFS(), m); err != nil {
		t.Fatalf("the shipped decision types must load: %v", err)
	}
}

func TestLoadRegistryRejects(t *testing.T) {
	head := "title: X\ntrigger: x\ndefault_rule: r\nseverity_weight: 1\n"
	need := "needs:\n  - {name: n, fetch: gmail.list_messages, kind: lookup}\n"
	cases := map[string]map[string]string{
		"bad yaml":             {"a.yaml": "id: [unclosed\n"},
		"missing id":           {"a.yaml": head + need},
		"missing title":        {"a.yaml": "id: a\ntrigger: x\ndefault_rule: r\nseverity_weight: 1\n" + need},
		"missing trigger":      {"a.yaml": "id: a\ntitle: X\ndefault_rule: r\nseverity_weight: 1\n" + need},
		"missing default_rule": {"a.yaml": "id: a\ntitle: X\ntrigger: x\nseverity_weight: 1\n" + need},
		"missing needs":        {"a.yaml": "id: a\n" + head},
		"zero severity":        {"a.yaml": "id: a\ntitle: X\ntrigger: x\ndefault_rule: r\n" + need},
		"unknown key":          {"a.yaml": "id: a\n" + head + need + "auto_approve: true\n"},
		"bad id":               {"a.yaml": "id: Budget Request\n" + head + need},
		"reserved id":          {"a.yaml": "id: generic\n" + head + need},
		"fetch not in manifest": {"a.yaml": "id: a\n" + head +
			"needs:\n  - {name: n, fetch: slack.list_messages, kind: lookup}\n"},
		"fetch is an action": {"a.yaml": "id: a\n" + head +
			"needs:\n  - {name: n, fetch: gmail.send_email, kind: lookup}\n"},
		"unregistered internal": {"a.yaml": "id: a\n" + head +
			"needs:\n  - {name: n, fetch: internal://nobody.compute, kind: computation}\n"},
		"internal not computation": {"a.yaml": "id: a\n" + head +
			"needs:\n  - {name: n, fetch: internal://test.sum, kind: lookup}\n"},
		"computation not internal": {"a.yaml": "id: a\n" + head +
			"needs:\n  - {name: n, fetch: gmail.list_messages, kind: computation}\n"},
		"bad kind": {"a.yaml": "id: a\n" + head +
			"needs:\n  - {name: n, fetch: gmail.list_messages, kind: guess}\n"},
		"unknown placeholder": {"a.yaml": "id: a\n" + head +
			"needs:\n  - {name: n, fetch: gmail.list_messages, kind: lookup, args: {query: \"{password}\"}}\n"},
		"duplicate need":            {"a.yaml": "id: a\n" + head + need + "  - {name: n, fetch: gdrive.search_files, kind: research}\n"},
		"empty staged action":       {"a.yaml": "id: a\n" + head + need + "staged_actions: [\"\"]\n"},
		"duplicate id across files": {"a.yaml": "id: a\n" + head + need, "b.yaml": "id: a\n" + head + need},
	}
	for name, files := range cases {
		t.Run(name, func(t *testing.T) {
			fsys := fstest.MapFS{}
			for f, body := range files {
				fsys["twins/t/decisions/"+f] = &fstest.MapFile{Data: []byte(body)}
			}
			if _, err := LoadRegistry(fsys, manifest(t, testManifest)); err == nil {
				t.Fatal("loaded a bad registry")
			}
		})
	}
	if _, err := LoadRegistry(fstest.MapFS{}, nil); err == nil {
		t.Fatal("loaded without a manifest")
	}
}

func TestGenericNeedsFollowTheManifest(t *testing.T) {
	m := manifest(t, "id: t\nname: T\nusage: {window: 1h, model_calls: 5}\n")
	r, err := LoadRegistry(fstest.MapFS{}, m)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Generic().Needs) != 0 {
		t.Fatalf("generic must only use granted functions: %+v", r.Generic().Needs)
	}
}

func modelClassifier(r *Registry, reply string, calls *atomic.Int64) *ModelClassifier {
	f := backend.NewFake("fake")
	f.Reply = func(backend.Request) string { calls.Add(1); return reply }
	return &ModelClassifier{Registry: r, Backend: f, Model: "haiku"}
}

func TestClassifyFallsBackToGeneric(t *testing.T) {
	r := registry(t, map[string]string{"budget.yaml": budgetYAML})
	item := msg("m1", true, "dana@x.com", "Keynote invite?")
	var n atomic.Int64
	cases := map[string]struct {
		reply string
		want  Classification
	}{
		"confident match":   {`{"needs_decision": true, "type_id": "budget", "confidence": 0.9}`, Classification{true, "budget", 0.9}},
		"below floor":       {`{"needs_decision": true, "type_id": "budget", "confidence": 0.3}`, Classification{true, GenericID, 0.3}},
		"unknown type":      {`Sure: {"needs_decision": true, "type_id": "keynote", "confidence": 0.99}`, Classification{true, GenericID, 0.99}},
		"explicit generic":  {"```json\n{\"needs_decision\": true, \"type_id\": \"generic\", \"confidence\": 0.9}\n```", Classification{true, GenericID, 0.9}},
		"no decision":       {`{"needs_decision": false, "type_id": "", "confidence": 0.8}`, Classification{false, GenericID, 0.8}},
		"garbage":           {"I think it's a budget thing", Classification{true, GenericID, 0}},
		"missing field":     {`{"type_id": "budget", "confidence": 0.9}`, Classification{true, GenericID, 0}},
		"confidence clamps": {`{"needs_decision": true, "type_id": "budget", "confidence": 7}`, Classification{true, "budget", 1}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := modelClassifier(r, tc.reply, &n).Classify(context.Background(), item)
			if err != nil || got != tc.want {
				t.Fatalf("got %+v %v, want %+v", got, err, tc.want)
			}
		})
	}
	mc := modelClassifier(r, `{"needs_decision": true, "type_id": "budget", "confidence": 0.6}`, &n)
	mc.Floor = 0.7
	if got, _ := mc.Classify(context.Background(), item); got.TypeID != GenericID {
		t.Fatalf("configured floor ignored: %+v", got)
	}
	req := mc.Backend.(*backend.Fake).Requests()[0]
	if !strings.Contains(req.Prompt, "- budget: someone is asking for money") || strings.Contains(req.Prompt, "Approve under") || req.Model != "haiku" {
		t.Fatalf("classifier must see only the index: %q", req.Prompt)
	}
	failing := &ModelClassifier{Registry: r, Backend: &backend.Fake{FailWith: errors.New("down")}}
	if _, err := failing.Classify(context.Background(), item); err == nil {
		t.Fatal("a failed model call must be an error")
	}
}

func TestGenericCardNeverDropsAnItem(t *testing.T) {
	r := registry(t, map[string]string{"budget.yaml": budgetYAML})
	items := []store.Record{
		msg("m1", true, "org@conf.example", "Keynote invite for October?"),
		msg("m2", true, "", ""),
		&store.Document{Meta: store.Meta{Source: "gdrive", SourceID: "d1", External: true}, Title: "Vendor renewal"},
		&store.Event{Meta: store.Meta{Source: "gcal", SourceID: "e1"}},
		&store.Contact{Meta: store.Meta{Source: "x", SourceID: "c1"}},
	}
	gates := []*fakeGate{
		{answers: map[string]func(gate.Call) (gate.Result, error){}},
		{answers: map[string]func(gate.Call) (gate.Result, error){"gmail.list_messages": none, "gdrive.search_files": none}},
		{answers: map[string]func(gate.Call) (gate.Result, error){"gmail.list_messages": found(msg("m9", true, "org@conf.example", "Earlier chat"))}},
	}
	for _, g := range gates {
		for _, item := range items {
			for _, typ := range []string{GenericID, "no_such_type", ""} {
				b := &Builder{Registry: r, Gate: g}
				c, err := b.Build(context.Background(), item, Classification{NeedsDecision: true, TypeID: typ})
				if err != nil {
					t.Fatal(err)
				}
				if c.TypeID != GenericID || (len(c.Gaps) == 0 && c.Question == "") || c.Recommendation != "" || c.Validate() != nil {
					t.Fatalf("bad generic card: %+v", c)
				}
				if len(c.SourceItemIDs) != 1 || c.SourceItemIDs[0] != Ref(item) {
					t.Fatalf("item not referenced: %+v", c.SourceItemIDs)
				}
			}
		}
	}
	if _, err := (&Builder{Registry: r, Gate: gates[0]}).Build(context.Background(), nil, Classification{}); err == nil {
		t.Fatal("nil item must be an error, not a panic")
	}
}

func TestEveryFigureHasASource(t *testing.T) {
	r := registry(t, map[string]string{"budget.yaml": budgetYAML, "investor.yaml": investorYAML})
	items := []store.Record{msg("m1", true, "dana@x.com", "Q3 marketing budget"), msg("m2", false, "me@x.com", "Offsite?"), &store.Document{Meta: store.Meta{Source: "gdrive", SourceID: "d1"}, Title: "Plan"}}
	answers := []func(gate.Call) (gate.Result, error){
		none,
		found(msg("h1", true, "dana@x.com", "Earlier ask"), msg("h2", true, "dana@x.com", "Another")),
		func(gate.Call) (gate.Result, error) { return gate.Result{}, errors.New("token expired") },
		found(&store.Document{Meta: store.Meta{Source: "gdrive", SourceID: "d7", External: true}, Title: "Budget"}),
	}
	for _, typ := range []string{"budget", "investor", GenericID} {
		for _, item := range items {
			for _, a := range answers {
				g := &fakeGate{answers: map[string]func(gate.Call) (gate.Result, error){"gmail.list_messages": a, "gdrive.search_files": a}}
				c, err := (&Builder{Registry: r, Gate: g}).Build(context.Background(), item, Classification{NeedsDecision: true, TypeID: typ})
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range c.Evidence {
					if e.Source == "" {
						t.Fatalf("%s: evidence without source: %+v", typ, e)
					}
				}
				for k := range c.Defaults {
					if c.DefaultSources[k] == "" {
						t.Fatalf("%s: default %s without source", typ, k)
					}
				}
				for k := range c.Parameters {
					if _, ok := c.Defaults[k]; !ok {
						t.Fatalf("%s: parameter %s without a default", typ, k)
					}
				}
			}
		}
	}
}

func TestSourcelessFigureIsRejected(t *testing.T) {
	hand := &Card{
		Evidence:       []Evidence{{Text: "invite", Source: "gmail:m1"}},
		Defaults:       map[string]any{"amount": 2000},
		DefaultSources: map[string]string{},
		Readiness:      Ready,
	}
	if err := hand.Validate(); !errors.Is(err, ErrUnsourced) {
		t.Fatalf("a sourceless figure must be rejected: %v", err)
	}
	hand = &Card{Evidence: []Evidence{{Text: "a number: 42"}}}
	if err := hand.Validate(); !errors.Is(err, ErrUnsourced) {
		t.Fatalf("sourceless evidence must be rejected: %v", err)
	}

	const y = "id: runway\ntitle: Runway\ntrigger: x\ndefault_rule: r\nseverity_weight: 1\nneeds:\n  - {name: runway, fetch: internal://test.unsourced, kind: computation}\n"
	r := registry(t, map[string]string{"runway.yaml": y})
	c, err := (&Builder{Registry: r, Gate: &fakeGate{}}).Build(context.Background(), msg("m1", false, "a@x", "Budget"), Classification{NeedsDecision: true, TypeID: "runway"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Readiness != MissingInfo {
		t.Fatalf("a card that lost a figure is not ready: %s", c.Readiness)
	}
	if _, ok := c.Defaults["invented"]; ok {
		t.Fatal("unsourced figure reached the card")
	}
	if _, ok := c.Defaults["forged"]; ok {
		t.Fatal("a figure citing a record this build never fetched reached the card")
	}
	if c.Defaults["runway_months"] != 7.5 || c.DefaultSources["runway_months"] != "code:runway" || c.Parameters["runway_months"] != 7.5 {
		t.Fatalf("sourced figure lost: %+v %+v", c.Defaults, c.DefaultSources)
	}
	if !strings.Contains(strings.Join(c.Gaps, "\n"), `"invented" was dropped`) {
		t.Fatalf("the drop must be stated as a gap: %q", c.Gaps)
	}
}

func TestReadiness(t *testing.T) {
	r := registry(t, map[string]string{"budget.yaml": budgetYAML})
	history := found(msg("h1", true, "dana@x.com", "Earlier ask"))
	cases := map[string]struct {
		answer func(gate.Call) (gate.Result, error)
		item   *store.Message
		want   Readiness
	}{
		"all found":        {history, msg("m1", true, "dana@x.com", "Budget"), Ready},
		"empty fetch":      {none, msg("m1", true, "dana@x.com", "Budget"), MissingInfo},
		"not found":        {func(gate.Call) (gate.Result, error) { return gate.Result{}, fmt.Errorf("x: %w", store.ErrNotFound) }, msg("m1", true, "dana@x.com", "Budget"), MissingInfo},
		"nothing to query": {history, msg("m1", true, "", "Budget"), MissingInfo},
		"gate denial":      {nil, msg("m1", true, "dana@x.com", "Budget"), Blocked},
		"connector error":  {func(gate.Call) (gate.Result, error) { return gate.Result{}, errors.New("token expired") }, msg("m1", true, "dana@x.com", "Budget"), Blocked},
		"only the item":    {func(c gate.Call) (gate.Result, error) { return found(msg("m1", true, "dana@x.com", "Budget"))(c) }, msg("m1", true, "dana@x.com", "Budget"), MissingInfo},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g := &fakeGate{answers: map[string]func(gate.Call) (gate.Result, error){}}
			if tc.answer != nil {
				g.answers["gmail.list_messages"] = tc.answer
			}
			c, err := (&Builder{Registry: r, Gate: g}).Build(context.Background(), tc.item, Classification{NeedsDecision: true, TypeID: "budget"})
			if err != nil {
				t.Fatal(err)
			}
			if c.Readiness != tc.want {
				t.Fatalf("readiness %s, want %s; gaps %q", c.Readiness, tc.want, c.Gaps)
			}
			if tc.want != Ready && len(c.Gaps) == 0 {
				t.Fatal("a card that is not ready must say why")
			}
		})
	}
	for _, fn := range []string{"test.fail", "test.panic"} {
		y := "id: c\ntitle: C\ntrigger: x\ndefault_rule: r\nseverity_weight: 1\nneeds:\n  - {name: n, fetch: internal://" + fn + ", kind: computation}\n"
		c, err := (&Builder{Registry: registry(t, map[string]string{"c.yaml": y}), Gate: &fakeGate{}}).Build(context.Background(), msg("m1", false, "a@x", "s"), Classification{TypeID: "c"})
		if err != nil || c.Readiness != Blocked {
			t.Fatalf("%s: a computation that can't run blocks the card: %v %+v", fn, err, c)
		}
	}
	if got := ReadinessOf(nil); got != Ready {
		t.Fatalf("no needs: %s", got)
	}
	if got := ReadinessOf([]NeedResult{{Outcome: OutcomeEmpty}, {Outcome: OutcomeError}, {Outcome: OutcomeFound}}); got != Blocked {
		t.Fatalf("error must dominate: %s", got)
	}
}

func TestFetchesGoThroughTheGateAsResearch(t *testing.T) {
	r := registry(t, map[string]string{"budget.yaml": budgetYAML})
	g := &fakeGate{answers: map[string]func(gate.Call) (gate.Result, error){"gmail.list_messages": none}}
	if _, err := (&Builder{Registry: r, Gate: g}).Build(context.Background(), msg("m1", true, "dana@x.com", "Budget"), Classification{TypeID: "budget"}); err != nil {
		t.Fatal(err)
	}
	if len(g.calls) != 1 {
		t.Fatalf("calls: %+v", g.calls)
	}
	c := g.calls[0]
	if c.Function != "gmail.list_messages" || c.Args["query"] != "from:dana@x.com" || c.Origin != gate.P1 || c.Taint != gate.Tainted || c.EnvelopeID != "" {
		t.Fatalf("call: %+v", c)
	}
	g.calls = nil
	if _, err := (&Builder{Registry: r, Gate: g, Origin: gate.P0}).Build(context.Background(), msg("m1", false, "me@x.com", "Budget"), Classification{TypeID: "budget"}); err != nil {
		t.Fatal(err)
	}
	if g.calls[0].Taint != gate.Clean || g.calls[0].Origin != gate.P0 {
		t.Fatalf("args from the CEO's own item are clean: %+v", g.calls[0])
	}
}

func TestStagedActionsAreNamedNotActionable(t *testing.T) {
	r := registry(t, map[string]string{"budget.yaml": budgetYAML})
	c, _ := (&Builder{Registry: r, Gate: &fakeGate{}}).Build(context.Background(), msg("m1", true, "a@x", "s"), Classification{TypeID: "budget"})
	if len(c.StagedActions) != 2 || !c.StagedActions[0].Actionable || c.StagedActions[1].Actionable {
		t.Fatalf("staged: %+v", c.StagedActions)
	}
}

func TestUntrustedFlag(t *testing.T) {
	r := registry(t, map[string]string{"budget.yaml": budgetYAML})
	build := func(item store.Record, a func(gate.Call) (gate.Result, error)) *Card {
		g := &fakeGate{answers: map[string]func(gate.Call) (gate.Result, error){"gmail.list_messages": a}}
		c, err := (&Builder{Registry: r, Gate: g}).Build(context.Background(), item, Classification{TypeID: "budget"})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	internalRec := func(gate.Call) (gate.Result, error) {
		return gate.Result{Records: []store.Record{msg("h1", false, "me@x.com", "mine")}}, nil
	}
	if !build(msg("m1", true, "dana@x.com", "Budget"), none).Untrusted {
		t.Fatal("an external email must make the card untrusted")
	}
	if !build(&store.Document{Meta: store.Meta{Source: "gdrive", SourceID: "d1", External: true}, Title: "Budget", Owner: "x@y"}, internalRec).Untrusted {
		t.Fatal("an external Drive doc must make the card untrusted")
	}
	if build(msg("m1", false, "me@x.com", "Budget"), internalRec).Untrusted {
		t.Fatal("nothing external went in, the card must not be untrusted")
	}
	if !build(msg("m1", false, "me@x.com", "Budget"), found(msg("h2", true, "dana@x.com", "theirs"))).Untrusted {
		t.Fatal("an external record resolved for a need must make the card untrusted")
	}
}

func TestTriagerClassifiesOnlyCandidatesOnce(t *testing.T) {
	r := registry(t, map[string]string{"budget.yaml": budgetYAML})
	var n atomic.Int64
	mc := modelClassifier(r, `{"needs_decision": true, "type_id": "budget", "confidence": 0.9}`, &n)
	if _, err := NewTriager(mc, nil); err == nil {
		t.Fatal("a triager without a candidate predicate would classify everything")
	}
	tr, err := NewTriager(mc, func(r store.Record) bool { m, ok := r.(*store.Message); return ok && strings.Contains(m.Subject, "?") })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, ok, _ := tr.Triage(ctx, msg("m0", true, "news@x", "Weekly digest")); ok || n.Load() != 0 {
		t.Fatal("a non-candidate must not be classified")
	}
	for i := 0; i < 3; i++ {
		c, ok, err := tr.Triage(ctx, msg("m1", true, "dana@x.com", "Approve the budget?"))
		if err != nil || !ok || c.TypeID != "budget" {
			t.Fatalf("%+v %v %v", c, ok, err)
		}
	}
	if n.Load() != 1 {
		t.Fatalf("classified %d times, want once", n.Load())
	}
	if _, hit := tr.Cached(msg("m1", true, "", "")); !hit {
		t.Fatal("cache is keyed by source identity")
	}
	tr.Forget("gmail:m1")
	tr.Triage(ctx, msg("m1", true, "dana@x.com", "Approve the budget?"))
	if n.Load() != 2 {
		t.Fatal("Forget must allow a fresh classification")
	}

	fails := &ModelClassifier{Registry: r, Backend: &backend.Fake{FailWith: errors.New("down")}}
	tr2, _ := NewTriager(fails, func(store.Record) bool { return true })
	if _, _, err := tr2.Triage(ctx, msg("m2", true, "a", "b?")); err == nil {
		t.Fatal("error must surface")
	}
	if _, hit := tr2.Cached(msg("m2", true, "", "")); hit {
		t.Fatal("a failed classification must not be cached")
	}
}

type fixedPhraser Prose

func (p fixedPhraser) Phrase(context.Context, Card, Type) (Prose, error) { return Prose(p), nil }

func TestPhraserCannotInventNumbersOrHideGaps(t *testing.T) {
	r := registry(t, map[string]string{"budget.yaml": budgetYAML})
	item := msg("m1", true, "dana@x.com", "Q3 budget of 1,500 for tools")
	g := &fakeGate{answers: map[string]func(gate.Call) (gate.Result, error){"gmail.list_messages": found(msg("h1", true, "dana@x.com", "Earlier ask"))}}
	p := fixedPhraser{
		Lead:           "Dana asks for $1,500 for tools",
		Question:       "Approve $9,999 for tools?",
		Options:        []Option{{"Approve", "Spends 1500"}, {"Decline", "Dana waits"}},
		Gaps:           []string{"Unclear which vendor."},
		Recommendation: "Approve: under the threshold.",
	}
	c, err := (&Builder{Registry: r, Gate: g, Phraser: p}).Build(context.Background(), item, Classification{TypeID: "budget"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Lead != "Dana asks for $1,500 for tools" {
		t.Fatalf("sourced lead rejected: %q", c.Lead)
	}
	if strings.Contains(c.Question, "9999") || strings.Contains(c.Question, "9,999") {
		t.Fatalf("invented number reached the card: %q", c.Question)
	}
	if len(c.Options) != 2 || c.Recommendation == "" || c.Readiness != Ready {
		t.Fatalf("sourced prose dropped: %+v", c)
	}
	if !strings.Contains(strings.Join(c.Gaps, "|"), "No deadline was found.") || !strings.Contains(strings.Join(c.Gaps, "|"), "Unclear which vendor.") {
		t.Fatalf("gaps must only grow: %q", c.Gaps)
	}

	gc, _ := (&Builder{Registry: r, Gate: g, Phraser: p}).Build(context.Background(), item, Classification{TypeID: GenericID})
	if gc.Recommendation != "" {
		t.Fatal("a generic card never carries a model recommendation")
	}
}

func TestRenderAndSpeak(t *testing.T) {
	r := registry(t, map[string]string{"budget.yaml": budgetYAML})
	g := &fakeGate{answers: map[string]func(gate.Call) (gate.Result, error){"gmail.list_messages": found(msg("h1", true, "dana@x.com", "Earlier ask"))}}
	c, _ := (&Builder{Registry: r, Gate: g}).Build(context.Background(), msg("m1", true, "dana@x.com", "Q3 **marketing** budget"), Classification{TypeID: "budget"})
	d := time.Date(2026, 10, 1, 17, 0, 0, 0, time.Local)
	c.Deadline = &d
	out := c.Render()
	for _, want := range []string{"[budget] Budget request: Q3", "readiness ready", "contains external content", "[gmail:h1]", "related_count: 1 [code:test.sum]", "gmail.send_email (needs your approval)", "gmail.draft_message (not yet available)", "Deadline: Thu 2026-10-01 17:00"} {
		if !strings.Contains(out, want) {
			t.Fatalf("render missing %q:\n%s", want, out)
		}
	}
	s := c.Speak()
	if strings.ContainsAny(s, "*\n#") || !strings.Contains(s, "Due Thursday, October 1") || !strings.Contains(s, "Everything needed") {
		t.Fatalf("speak: %q", s)
	}
}

// TestRealGate drives the builder through a real gate: a read at level R
// succeeds, and a call the gate refuses (not on P2's allowlist) blocks the
// card instead of failing the build.
func TestRealGate(t *testing.T) {
	dir := t.TempDir()
	m := manifest(t, "id: t\nname: T\nusage: {window: 1h, model_calls: 5}\nconnectors:\n  - name: fake_mail\n    functions:\n      - {name: list_messages, level: R}\n  - name: fake_docs\n    functions:\n      - {name: read_doc, level: R}\nauto_allowlist: [fake_mail.list_messages]\n")
	reg, err := connectors.NewRegistry(fake.NewMail(fake.Message{ID: "x1", From: "dana@x.com", Subject: "Earlier budget", Body: "hi"}), fake.NewDocs())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(filepath.Join(dir, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	v := vault.NewMemory()
	v.Set(fake.MailService, fake.MailAccount, vault.NewSecret("tok"))
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: approvals.NewQueue(st, log), Audit: log, Vault: v, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{
		"twins/t/decisions/mail.yaml": {Data: []byte("id: mail\ntitle: Mail\ntrigger: x\ndefault_rule: r\nseverity_weight: 1\nneeds:\n  - {name: inbox, fetch: fake_mail.list_messages, kind: lookup}\n")},
		"twins/t/decisions/doc.yaml":  {Data: []byte("id: doc\ntitle: Doc\ntrigger: x\ndefault_rule: r\nseverity_weight: 1\nneeds:\n  - {name: inbox, fetch: fake_mail.list_messages, kind: lookup}\n  - {name: doc, fetch: fake_docs.read_doc, kind: lookup, args: {id: \"{source_id}\"}}\n")},
	}
	r, err := LoadRegistry(fsys, m)
	if err != nil {
		t.Fatal(err)
	}
	item := msg("m1", true, "dana@x.com", "Budget")
	c, err := (&Builder{Registry: r, Gate: g, Origin: gate.P2}).Build(context.Background(), item, Classification{TypeID: "mail"})
	if err != nil || c.Readiness != Ready || !c.Untrusted {
		t.Fatalf("real read: %v %+v", err, c)
	}
	c, err = (&Builder{Registry: r, Gate: g, Origin: gate.P2}).Build(context.Background(), item, Classification{TypeID: "doc"})
	if err != nil || c.Readiness != Blocked || !strings.Contains(strings.Join(c.Gaps, " "), "auto allowlist") {
		t.Fatalf("gate denial must block: %v %+v", err, c)
	}
}
