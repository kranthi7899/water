package promote

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"testing/fstest"

	"water/internal/nervous/intents"
	"water/internal/nervous/slots"
	"water/internal/store"
	"water/internal/twins"
)

func TestWriteLearnedPathAndPermissions(t *testing.T) {
	home := t.TempDir()
	path, err := WriteLearned(home, "ceo", []byte("id: learned.x\n"), "learned.x")
	if err != nil {
		t.Fatalf("WriteLearned: %v", err)
	}
	want := filepath.Join(home, "twins", "ceo", "intents", "learned", "learned.x.yaml")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(b) != "id: learned.x\n" {
		t.Fatalf("content = %q, want the original bytes", b)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file perm = %v, want 0600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("dir perm = %v, want 0700", dirInfo.Mode().Perm())
	}
}

func TestWritePendingPath(t *testing.T) {
	home := t.TempDir()
	path, err := WritePending(home, "ceo", []byte("id: learned.x\n"), "abc123def456")
	if err != nil {
		t.Fatalf("WritePending: %v", err)
	}
	want := filepath.Join(home, "twins", "ceo", "intents", "pending", "abc123def456.yaml")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
}

func TestWriteLearnedCreatesDirectory(t *testing.T) {
	home := t.TempDir()
	dir := LearnedDir(home, "ceo")
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("test setup: the learned dir should not exist yet")
	}
	if _, err := WriteLearned(home, "ceo", []byte("id: learned.x\n"), "learned.x"); err != nil {
		t.Fatalf("WriteLearned: %v", err)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("LearnedDir was not created: %v", err)
	}
}

const overlayManifestYAML = `
id: testtwin
name: Test twin
usage: {window: 5h, model_calls: 200, auto_model_calls: 40}
models: {fast: haiku, strong: ""}
connectors:
  - name: gcal
    functions:
      - {name: list_events, level: R}
`

const overlaySharedYAML = `
rules: {}
skip_words: []
deny_words: []
escalate_words: []
clause_joiners: []
`

const overlayEmbeddedYAML = `
id: schedule.on_date
description: Events on a given day
function: store.next_event
templates:
  - "what's on my calendar"
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
tests:
  - {utterance: "what's on my calendar", intent: schedule.on_date}
  - {utterance: "gibberish", intent: "none"}
`

func overlayFunctions() intents.Functions {
	return intents.Functions{Read: map[string]intents.FunctionSpec{
		"store.next_event": {ID: "store.next_event", Args: map[string]slots.Type{}, ReadOnly: true, Deterministic: true, Class: intents.ClassLookup},
	}}
}

// TestEmbeddedWinsOverLearnedRealOverlayPath writes a learned file that
// collides with an embedded intent's own id through the REAL WriteLearned
// path, then loads it back via os.DirFS(LearnedDir(...)) — exactly the way
// the daemon's own reload wiring does (internal/cli/cmd_daemon.go's
// daemonIntentsReloader) — rather than an in-memory fstest.MapFS unit test
// (intents.TestLoadRegistryLearnedEmbeddedWins already covers the pure
// registry-level mechanics; this proves THIS task's overlay-writing/reading
// path specifically).
func TestEmbeddedWinsOverLearnedRealOverlayPath(t *testing.T) {
	home := t.TempDir()
	const twinID = "testtwin"

	collidingYAML := []byte(`
id: schedule.on_date
description: a learned file trying to reuse an embedded id
function: store.next_event
templates: ["some colliding phrase"]
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
origin: learned
tests:
  - {utterance: "some colliding phrase", intent: schedule.on_date}
  - {utterance: "bye", intent: "none"}
`)
	if _, err := WriteLearned(home, twinID, collidingYAML, "schedule.on_date"); err != nil {
		t.Fatalf("WriteLearned: %v", err)
	}

	m, err := twins.Parse([]byte(overlayManifestYAML))
	if err != nil {
		t.Fatalf("twins.Parse: %v", err)
	}
	fsys := fstest.MapFS{
		"twins/testtwin/intents/_shared.yaml":          {Data: []byte(overlaySharedYAML)},
		"twins/testtwin/intents/schedule_on_date.yaml": {Data: []byte(overlayEmbeddedYAML)},
	}
	reg, err := intents.LoadRegistry(fsys, m, overlayFunctions(), intents.LoadOptions{Learned: os.DirFS(LearnedDir(home, twinID))})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	skipped := reg.LearnedSkipped()
	if len(skipped) != 1 {
		t.Fatalf("LearnedSkipped() = %+v, want exactly 1 (the colliding file)", skipped)
	}
	it, ok := reg.Lookup("schedule.on_date")
	if !ok {
		t.Fatal("schedule.on_date not found at all")
	}
	if it.Origin != "" {
		t.Fatalf("schedule.on_date.Origin = %q, want \"\" (the embedded version must remain active, never the learned one)", it.Origin)
	}
}

// TestOverlayNotLoadedWithFlagOff proves "with the flag off, the overlay is
// not loaded at all" (Design §16 item 4) at the loader level: a caller that
// simply never sets LoadOptions.Learned (the router.promotion.enabled=false
// case, as internal/cli/cmd_daemon.go's daemonIntentsReloader implements it)
// gets a registry with no learned intents whatsoever, even though a real,
// perfectly valid learned file sits on disk right where the overlay would
// have looked.
func TestOverlayNotLoadedWithFlagOff(t *testing.T) {
	home := t.TempDir()
	const twinID = "testtwin"

	validLearnedYAML := []byte(`
id: learned.next_thing
description: a valid learned intent
function: store.next_event
templates: ["what's coming up after this"]
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
origin: learned
tests:
  - {utterance: "what's coming up after this", intent: learned.next_thing}
  - {utterance: "bye", intent: "none"}
`)
	if _, err := WriteLearned(home, twinID, validLearnedYAML, "learned.next_thing"); err != nil {
		t.Fatalf("WriteLearned: %v", err)
	}

	m, err := twins.Parse([]byte(overlayManifestYAML))
	if err != nil {
		t.Fatalf("twins.Parse: %v", err)
	}
	fsys := fstest.MapFS{
		"twins/testtwin/intents/_shared.yaml":          {Data: []byte(overlaySharedYAML)},
		"twins/testtwin/intents/schedule_on_date.yaml": {Data: []byte(overlayEmbeddedYAML)},
	}
	// LoadOptions.Learned left nil: the flag-off case.
	reg, err := intents.LoadRegistry(fsys, m, overlayFunctions(), intents.LoadOptions{})
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if _, ok := reg.Lookup("learned.next_thing"); ok {
		t.Fatal("learned.next_thing loaded even though LoadOptions.Learned was nil (flag off)")
	}
	if len(reg.LearnedSkipped()) != 0 {
		t.Fatalf("LearnedSkipped() = %+v, want none (nothing was even attempted)", reg.LearnedSkipped())
	}
}

// TestManualDemoteAndEnableRoundTrip exercises "manual demote and enable"
// end to end through the real mechanisms this task wires together for the
// first time: store.SetIntentState/ListIntentStates (R-4, unmodified) and
// intents.LoadRegistry's LoadOptions.Disabled (R-6, previously never fed by
// a real store anywhere in this codebase — see cmd_daemon.go's
// daemonIntentsReloader, which is the real, wired caller this task adds).
func TestManualDemoteAndEnableRoundTrip(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "water.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	const id = "learned.next_thing"
	now := time.Now()

	if err := st.SetIntentState(ctx, id, true, "manual: too noisy", now); err != nil {
		t.Fatalf("SetIntentState(disable): %v", err)
	}
	disabled, err := st.ListIntentStates(ctx)
	if err != nil {
		t.Fatalf("ListIntentStates: %v", err)
	}
	if disabled[id] != "manual: too noisy" {
		t.Fatalf("disabled[%s] = %q, want the manual reason", id, disabled[id])
	}

	m, err := twins.Parse([]byte(overlayManifestYAML))
	if err != nil {
		t.Fatalf("twins.Parse: %v", err)
	}
	fsys := fstest.MapFS{
		"twins/testtwin/intents/_shared.yaml":          {Data: []byte(overlaySharedYAML)},
		"twins/testtwin/intents/schedule_on_date.yaml": {Data: []byte(overlayEmbeddedYAML)},
	}
	learned := mapFSOf("next_thing.yaml", `
id: learned.next_thing
description: a valid learned intent
function: store.next_event
templates: ["what's coming up after this"]
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
origin: learned
tests:
  - {utterance: "what's coming up after this", intent: learned.next_thing}
  - {utterance: "bye", intent: "none"}
`)

	regDisabled, err := intents.LoadRegistry(fsys, m, overlayFunctions(), intents.LoadOptions{Learned: learned, Disabled: disabled})
	if err != nil {
		t.Fatalf("LoadRegistry (disabled): %v", err)
	}
	for _, c := range regDisabled.Candidates() {
		if c.ID == id {
			t.Fatalf("%s is in Candidates() while disabled", id)
		}
	}
	it, ok := regDisabled.Lookup(id)
	if !ok || it.Disabled == "" {
		t.Fatalf("%s: Disabled = %q, want the manual reason to be set", id, it.Disabled)
	}

	// Enable: SetIntentState(disabled=false) deletes the row.
	if err := st.SetIntentState(ctx, id, false, "", now); err != nil {
		t.Fatalf("SetIntentState(enable): %v", err)
	}
	disabled, err = st.ListIntentStates(ctx)
	if err != nil {
		t.Fatalf("ListIntentStates: %v", err)
	}
	if _, stillThere := disabled[id]; stillThere {
		t.Fatalf("%s still reported disabled after enabling", id)
	}

	regEnabled, err := intents.LoadRegistry(fsys, m, overlayFunctions(), intents.LoadOptions{Learned: learned, Disabled: disabled})
	if err != nil {
		t.Fatalf("LoadRegistry (enabled): %v", err)
	}
	found := false
	for _, c := range regEnabled.Candidates() {
		if c.ID == id {
			found = true
		}
	}
	if !found {
		t.Fatalf("%s not back in Candidates() after enabling", id)
	}
}
