package cli

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"water"
	"water/internal/connectors/fake"
	"water/internal/connectors/github"
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
