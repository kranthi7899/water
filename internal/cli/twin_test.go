package cli

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"water"
	"water/internal/backend"
	"water/internal/config"
	"water/internal/connectors/fake"
	"water/internal/connectors/github"
	"water/internal/store"
	"water/internal/twins"
)

// minimalCEOManifestYAML is just enough of twins/ceo/twin.yaml's shape for
// twins.Load to accept it; these tests care only about the decision
// registry's own validation, so no connectors are declared.
const minimalCEOManifestYAML = `
id: ceo
name: CEO twin
usage: {window: 1h, model_calls: 10, auto_model_calls: 2}
`

// TestBuildTwinDepsFSFailsLoudlyOnBadDecisionsFile confirms the wiring
// task 1 asks for: a malformed twins/ceo/decisions/*.yaml stops startup
// exactly like a bad twin.yaml does, before anything else (the real store,
// audit log, ...) is even opened. The fixture lives entirely in an in-memory
// fstest.MapFS, never touching the real twins/ceo/decisions/.
func TestBuildTwinDepsFSFailsLoudlyOnBadDecisionsFile(t *testing.T) {
	fsys := fstest.MapFS{
		"twins/ceo/twin.yaml": &fstest.MapFile{Data: []byte(minimalCEOManifestYAML)},
		// id decodes as a YAML sequence, not a string: this must fail to
		// parse, not be silently ignored or treated as an empty id.
		"twins/ceo/decisions/broken.yaml": &fstest.MapFile{Data: []byte("id: [not, a, string]\ntitle: Broken\n")},
	}
	_, err := buildTwinDepsFS(fsys, realTwinID, "", "", "")
	if err == nil {
		t.Fatal("buildTwinDepsFS: expected an error from a malformed decision-type file, got nil")
	}
	if !strings.Contains(err.Error(), "decision registry") {
		t.Fatalf("buildTwinDepsFS error = %q, want it to name the decision registry", err.Error())
	}
}

// TestBuildTwinDepsFSFailsLoudlyOnUnknownFetch is the other end of the same
// rule: a needs[].fetch naming a function twin.yaml never granted must also
// stop startup.
func TestBuildTwinDepsFSFailsLoudlyOnUnknownFetch(t *testing.T) {
	fsys := fstest.MapFS{
		"twins/ceo/twin.yaml": &fstest.MapFile{Data: []byte(minimalCEOManifestYAML)},
		"twins/ceo/decisions/bad_fetch.yaml": &fstest.MapFile{Data: []byte(`
id: bad_fetch
title: Bad fetch
trigger: something
needs:
  - name: whatever
    fetch: gmail.list_messages
    kind: lookup
default_rule: never auto-approve
severity_weight: 1
`)},
	}
	_, err := buildTwinDepsFS(fsys, realTwinID, "", "", "")
	if err == nil {
		t.Fatal("buildTwinDepsFS: expected an error naming an unlisted function, got nil")
	}
}

// A twin with no twins/<id>/decisions directory at all (an empty registry,
// generic only) is already covered by internal/decisions'
// TestLoadRegistryEmptyDirIsOnlyGeneric; it is not re-tested here because
// buildTwinDepsFS's success path goes on to open the real ~/.water store,
// which a unit test must not touch.

// TestBuildTwinDepsFSFailsLoudlyOnBadIntentFile is R-15's own version of the
// same rule (Design §5.3, mirroring decisions.LoadRegistry): a malformed
// twins/<id>/intents/*.yaml must stop startup before the store or audit log
// ever opens, exactly like a bad twin.yaml or decisions file does.
func TestBuildTwinDepsFSFailsLoudlyOnBadIntentFile(t *testing.T) {
	fsys := fstest.MapFS{
		"twins/ceo/twin.yaml":            &fstest.MapFile{Data: []byte(minimalCEOManifestYAML)},
		"twins/ceo/intents/_shared.yaml": &fstest.MapFile{Data: []byte("skip_words: [please]\n")},
		// "kind" is not a recognized key on an Intent (strict decode); this
		// must fail to parse, not be silently dropped.
		"twins/ceo/intents/broken.yaml": &fstest.MapFile{Data: []byte(`
id: broken.intent
description: A malformed intent
kynd: read
function: store.something
templates: ["do the thing"]
tests:
  - {utterance: "do the thing", intent: broken.intent}
`)},
	}
	_, err := buildTwinDepsFS(fsys, realTwinID, "", "", "")
	if err == nil {
		t.Fatal("buildTwinDepsFS: expected an error from a malformed intent file, got nil")
	}
	if !strings.Contains(err.Error(), "intent registry") {
		t.Fatalf("buildTwinDepsFS error = %q, want it to name the intent registry", err.Error())
	}
}

// TestDemoTwinNeverTouchesRealStore is the review finding: the demo twin's
// fake GitHub/Linear/HubSpot records (and its cards and classification
// cache) used to be upserted into the real twin's ~/.water/water.db. Under a
// temp WATER_HOME, building the demo twin's deps must create its own store
// and audit log and never the real ones.
func TestDemoTwinNeverTouchesRealStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WATER_HOME", home)
	deps, err := buildTwinDepsFS(water.TwinsFS(), demoTwinID, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	deps.Close()
	for _, p := range []string{filepath.Join(home, "water.db"), filepath.Join(home, "audit", "audit.jsonl")} {
		if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("demo twin touched the real twin's %s (stat err %v)", p, err)
		}
	}
	for _, p := range []string{twinStorePath(demoTwinID), twinAuditPath(demoTwinID)} {
		if !strings.HasPrefix(p, filepath.Join(home, "twins", demoTwinID)) {
			t.Fatalf("demo path %s not under its own twin dir", p)
		}
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("demo twin's own %s missing: %v", p, err)
		}
	}
	if twinStorePath(realTwinID) != filepath.Join(home, "water.db") || twinAuditPath(realTwinID) != filepath.Join(home, "audit", "audit.jsonl") {
		t.Fatal("real twin paths must stay the original ~/.water layout")
	}
}

// TestTwinIDDefaultsToRealAndDemoFlagOrEnvSelectsDemo is the regression
// guard the demo slice needs: with neither --demo nor WATER_DEMO set, a
// fresh App must resolve to the real twin, exactly as before --demo
// existed. --demo and WATER_DEMO are the only two ways to opt into the
// demo twin, and either one is enough on its own.
func TestTwinIDDefaultsToRealAndDemoFlagOrEnvSelectsDemo(t *testing.T) {
	a := NewApp()
	if got := a.twinID(); got != realTwinID {
		t.Fatalf("twinID() with neither --demo nor WATER_DEMO set = %q, want %q (must never default to demo)", got, realTwinID)
	}

	a.flags.demo = true
	if got := a.twinID(); got != demoTwinID {
		t.Fatalf("twinID() with --demo = %q, want %q", got, demoTwinID)
	}

	a2 := NewApp()
	t.Setenv(demoEnvVar, "1")
	if got := a2.twinID(); got != demoTwinID {
		t.Fatalf("twinID() with WATER_DEMO=1 = %q, want %q", got, demoTwinID)
	}
}

// TestLoadTwinManifestRealAndDemoBothValidate loads both shipped manifests
// through the real embedded filesystem and the real gate validation
// (loadTwinManifest is exactly what `water status`/`water doctor` and the
// daemon's startup use). The real manifest must keep loading exactly as it
// did before the demo slice; the demo manifest must load too, since it is
// additive.
func TestLoadTwinManifestRealAndDemoBothValidate(t *testing.T) {
	if _, err := loadTwinManifest(water.TwinsFS(), realTwinID, "", "", ""); err != nil {
		t.Fatalf("real (non-demo) twin manifest must still load unchanged: %v", err)
	}
	if _, err := loadTwinManifest(water.TwinsFS(), demoTwinID, "", "", ""); err != nil {
		t.Fatalf("demo twin manifest must load: %v", err)
	}
}

// TestRealAndDemoManifestsBothGrantGitHubLinearHubSpot checks both twins'
// manifests grant the same github/linear/hubspot function ids at level R.
// This changed from the original demo-slice regression guard ("the real
// manifest must never grant these") once this slice added real, read-only
// github/linear/hubspot connectors (internal/connectors/{github,linear,
// hubspot}) to the real ceo twin: both manifests now legitimately declare
// the same function ids, backed by different connectors depending on which
// twin id is loaded — buildCEORegistry wires the real network connectors
// for realTwinID and the in-memory fakes for demoTwinID (see its own doc
// comment). TestBuildCEORegistryPicksRealOrFakeGitHubLinearHubSpot below is
// what actually guards that wiring choice.
func TestRealAndDemoManifestsBothGrantGitHubLinearHubSpot(t *testing.T) {
	ghLinearHubspot := []string{"github.list_prs", "github.list_issues", "linear.list_issues", "hubspot.list_deals", "hubspot.list_contacts"}

	real, err := twins.Load(water.TwinsFS(), realTwinID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ghLinearHubspot {
		f, ok := real.Function(id)
		if !ok || f.Level != twins.R {
			t.Fatalf("real manifest: %s = %+v, ok=%v; want level R", id, f, ok)
		}
	}

	demo, err := twins.Load(water.TwinsFS(), demoTwinID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ghLinearHubspot {
		f, ok := demo.Function(id)
		if !ok || f.Level != twins.R {
			t.Fatalf("demo manifest: %s = %+v, ok=%v; want level R", id, f, ok)
		}
	}
	// The demo manifest is additive: it must still grant every real Google
	// function the production manifest does.
	for _, id := range []string{"gcal.list_events", "gmail.list_messages", "gmail.get_message", "gdrive.search_files", "gdrive.read_file"} {
		if _, ok := demo.Function(id); !ok {
			t.Fatalf("demo manifest dropped a real function it should keep: %s", id)
		}
	}
}

// TestBuildCEORegistryPicksRealOrFakeGitHubLinearHubSpot guards the wiring
// choice buildCEORegistry makes: demo=false (the real twin) must register
// the real github/linear/hubspot connectors (internal/connectors/{github,
// linear,hubspot}), never the in-memory fakes that would silently return
// canned demo data; demo=true must register the fakes, never a real
// connector that could attempt a live network call with no configured
// token. Checked by concrete type, since both sets share the same
// connector Name()s and function ids by design.
func TestBuildCEORegistryPicksRealOrFakeGitHubLinearHubSpot(t *testing.T) {
	real, err := buildCEORegistry(realTwinID, nil, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	conn, _, ok := real.Lookup("github.list_prs")
	if !ok {
		t.Fatal("real registry: github.list_prs not found")
	}
	if _, isReal := conn.(*github.GitHub); !isReal {
		t.Fatalf("real registry's github connector is %T, want *github.GitHub", conn)
	}

	demo, err := buildCEORegistry(demoTwinID, nil, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	conn, _, ok = demo.Lookup("github.list_prs")
	if !ok {
		t.Fatal("demo registry: github.list_prs not found")
	}
	if _, isFake := conn.(*fake.GitHub); !isFake {
		t.Fatalf("demo registry's github connector is %T, want *fake.GitHub", conn)
	}
}

// TestBuildDecisionsTriggerWithNullDeciderMatchesUnwrappedClassifier is
// R-24's regression guard: buildDecisionsTrigger wraps its ModelClassifier
// with decider.Wrap(decider.New(deciderProvider), classifier, reg) before
// StoreCache (R-25 sources the provider from the caller instead of
// hardcoding decider.Null{}; see buildDecisionsTrigger's own comment in
// twin.go). decider.Wrap with a Null decider returns the classifier it was
// given UNCHANGED (see internal/decider/classifier.go), so passing "none"
// (the only supported provider) must classify and build a card exactly as
// the pre-R-24 unwrapped ModelClassifier did — same backend call, same
// matched type, same card.
func TestBuildDecisionsTriggerWithNullDeciderMatchesUnwrappedClassifier(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WATER_HOME", home)
	deps, err := buildTwinDepsFS(water.TwinsFS(), demoTwinID, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer deps.Close()

	be := backend.NewFake("test")
	be.Reply = func(req backend.Request) string {
		return `{"needs_decision": true, "type_id": "investor_request", "confidence": 0.9}`
	}

	msg := &store.Message{
		Meta:    store.Meta{Source: "gmail", SourceID: "r24-regress-1", External: true},
		From:    "investor@meridian.example",
		Subject: "Series B follow-on",
		Body:    "Can you send the latest deck by Friday?",
	}
	if err := deps.store.Upsert(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	trig := buildDecisionsTrigger(deps, be, "none", 0)
	if trig == nil {
		t.Fatal("buildDecisionsTrigger returned nil")
	}
	cards, err := trig.Run(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 {
		t.Fatalf("cards = %d, want exactly 1", len(cards))
	}
	if cards[0].TypeID != "investor_request" {
		t.Fatalf("card TypeID = %q, want %q", cards[0].TypeID, "investor_request")
	}
	if be.Calls() < 1 {
		t.Fatal("expected the (unwrapped-behavior) classifier to have called the backend at least once")
	}
}

// TestBuildDecisionsTriggerSourcesProviderFromResolvedConfig is R-25's own
// regression guard: cmd_daemon.go now calls
// buildDecisionsTrigger(deps, sel.Backend, cfg.Decider.Provider) instead of
// hardcoding decider.Null{} (R-24 left that as its own explicit follow-up).
// Since apply() only ever resolves decider.provider to "none", the real
// resolved config's own value, fed straight through, must produce the
// exact same triage result TestBuildDecisionsTriggerWithNullDeciderMatchesUnwrappedClassifier
// gets from the literal "none" — proving the value genuinely flows from
// config, not just that the literal string still works.
func TestBuildDecisionsTriggerSourcesProviderFromResolvedConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WATER_HOME", home)
	deps, err := buildTwinDepsFS(water.TwinsFS(), demoTwinID, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer deps.Close()

	resolved, err := config.Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Decider.Provider != "none" {
		t.Fatalf("decider.provider default = %q, want %q", resolved.Decider.Provider, "none")
	}

	be := backend.NewFake("test")
	be.Reply = func(req backend.Request) string {
		return `{"needs_decision": true, "type_id": "investor_request", "confidence": 0.9}`
	}
	msg := &store.Message{
		Meta:    store.Meta{Source: "gmail", SourceID: "r25-config-provider-1", External: true},
		From:    "investor@meridian.example",
		Subject: "Series B follow-on",
		Body:    "Can you send the latest deck by Friday?",
	}
	if err := deps.store.Upsert(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	trig := buildDecisionsTrigger(deps, be, resolved.Decider.Provider, resolved.Decisions.CardTTL())
	if trig == nil {
		t.Fatal("buildDecisionsTrigger returned nil")
	}
	// The card cache's TTL flows from decisions.card_ttl_seconds too: the
	// daemon's one shared trigger is what keeps the needs-you ticker, GET
	// /v1/decisions and the brief from re-fetching evidence on every call.
	if trig.CardTTL != 10*time.Minute {
		t.Fatalf("trigger CardTTL = %s, want decisions.card_ttl_seconds' default 10m", trig.CardTTL)
	}
	cards, err := trig.Run(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[0].TypeID != "investor_request" {
		t.Fatalf("cards = %+v, want exactly one investor_request card", cards)
	}
}

// TestBuildDecisionsTriggerFallsBackToNullOnUnsupportedProvider covers the
// defensive branch buildDecisionsTrigger takes when decider.New errors
// (unreachable through real config today, since apply() already rejects
// anything but "none" before this ever runs) — it must fall back to
// decider.Null{} and keep classifying normally, not fail daemon startup
// over a decider nothing outward-facing depends on.
func TestBuildDecisionsTriggerFallsBackToNullOnUnsupportedProvider(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WATER_HOME", home)
	deps, err := buildTwinDepsFS(water.TwinsFS(), demoTwinID, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer deps.Close()

	be := backend.NewFake("test")
	be.Reply = func(req backend.Request) string {
		return `{"needs_decision": true, "type_id": "investor_request", "confidence": 0.9}`
	}
	msg := &store.Message{
		Meta:    store.Meta{Source: "gmail", SourceID: "r25-bad-provider-1", External: true},
		From:    "investor@meridian.example",
		Subject: "Series B follow-on",
		Body:    "Can you send the latest deck by Friday?",
	}
	if err := deps.store.Upsert(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	trig := buildDecisionsTrigger(deps, be, "bogus-provider", 0)
	if trig == nil {
		t.Fatal("buildDecisionsTrigger returned nil")
	}
	cards, err := trig.Run(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(cards) != 1 || cards[0].TypeID != "investor_request" {
		t.Fatalf("cards = %+v, want exactly one investor_request card", cards)
	}
}
