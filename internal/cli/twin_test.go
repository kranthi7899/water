package cli

import (
	"strings"
	"testing"
	"testing/fstest"
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
	_, err := buildTwinDepsFS(fsys)
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
	_, err := buildTwinDepsFS(fsys)
	if err == nil {
		t.Fatal("buildTwinDepsFS: expected an error naming an unlisted function, got nil")
	}
}

// A twin with no twins/<id>/decisions directory at all (an empty registry,
// generic only) is already covered by internal/decisions'
// TestLoadRegistryEmptyDirIsOnlyGeneric; it is not re-tested here because
// buildTwinDepsFS's success path goes on to open the real ~/.water store,
// which a unit test must not touch.
