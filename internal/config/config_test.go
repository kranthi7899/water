package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestLayersAndProvenance(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WATER_HOME", home)
	os.MkdirAll(home, 0o755)
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("schema: 1\nbackend:\n  preferred: codex-subscription\nvoice:\n  model: gpt-4o-mini-tts\n"), 0o600)
	t.Setenv("WATER_VOICE_MODEL", "custom-model")

	r, err := Load(map[string]string{"backend.allow_metered": "true"})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][2]string{
		"backend.preferred":     {"codex-subscription", LayerFile},
		"voice.model":           {"custom-model", LayerEnv},
		"backend.allow_metered": {"true", LayerFlag},
		"voice.provider":        {"os", LayerDefault},
	}
	flat := r.Flat()
	for k, want := range cases {
		if flat[k] != want[0] || r.Provenance[k] != want[1] {
			t.Errorf("%s = %q from %s; want %q from %s", k, flat[k], r.Provenance[k], want[0], want[1])
		}
	}
	if r.Voice.Model != "custom-model" || !r.Backend.AllowMetered {
		t.Fatal("typed config not applied")
	}
}

func TestUnknownKeyAndBadSchema(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WATER_HOME", home)
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("schema: 1\nbogus: 1\n"), 0o600)
	if _, err := Load(nil); err == nil {
		t.Fatal("unknown key should fail")
	}
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("schema: 99\n"), 0o600)
	if _, err := Load(nil); err == nil {
		t.Fatal("newer schema should fail")
	}
}

// TestRetiredKeysStillLoad is the council-era config.yaml compatibility case:
// a file with the removed orchestration/sessions/tools/skills/ui/telemetry/
// memory blocks and per-role voices must still load, silently dropping them.
func TestRetiredKeysStillLoad(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WATER_HOME", home)
	old := "schema: 1\n" +
		"orchestration:\n  router: hierarchy\n  checkpoint_dir: /tmp/x\n" +
		"telemetry:\n  trace_dir: /tmp/traces\n" +
		"sessions:\n  keep: 30\n" +
		"tools:\n  enabled: true\n" +
		"skills:\n  selector: keyword\n" +
		"ui:\n  theme: dark\n" +
		"memory:\n  provider: markdown\n" +
		"voice:\n  ceo_voice: marin\n  coo_voice: cedar\n  cto_voice: ash\n  design_voice: coral\n"
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte(old), 0o600)
	r, err := Load(nil)
	if err != nil {
		t.Fatalf("old config with retired keys should still load: %v", err)
	}
	if r.Voice.CEOVoice != "marin" {
		t.Fatalf("surviving key lost: ceo_voice = %q", r.Voice.CEOVoice)
	}
}

func TestSaveMerges(t *testing.T) {
	t.Setenv("WATER_HOME", t.TempDir())
	if err := Save(map[string]string{"backend.preferred": "api", "backend.allow_metered": "true"}); err != nil {
		t.Fatal(err)
	}
	if err := Save(map[string]string{"voice.provider": "noop"}); err != nil {
		t.Fatal(err)
	}
	r, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Backend.Preferred != "api" || !r.Backend.AllowMetered || r.Provenance["voice.provider"] != LayerFile {
		t.Fatalf("merge lost values: %+v", r.Flat())
	}
}

// TestSaveRejectsInvalidValues: a value Load would reject (or that silently
// breaks a consumer) must be refused at set time, leaving the file untouched.
func TestSaveRejectsInvalidValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WATER_HOME", home)
	if err := Save(map[string]string{"voice.provider": "noop"}); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(Path())
	for k, v := range map[string]string{
		"sync.interval_minutes": "ten",
		"voice.allow_metered":   "maybe",
		"schema":                "7",
		"brief.ready_after":     "7am",
	} {
		if err := Save(map[string]string{k: v}); err == nil {
			t.Errorf("Save(%s=%q) succeeded; want an error", k, v)
		}
		after, _ := os.ReadFile(Path())
		if string(after) != string(before) {
			t.Fatalf("Save(%s=%q) changed the file:\n%s", k, v, after)
		}
	}
	if _, err := Load(nil); err != nil {
		t.Fatalf("config no longer loads: %v", err)
	}
	if err := Save(map[string]string{"brief.ready_after": "06:30"}); err != nil {
		t.Fatalf("valid HH:MM refused: %v", err)
	}
}

func TestLoadRejectsBadReadyAfter(t *testing.T) {
	t.Setenv("WATER_HOME", t.TempDir())
	if _, err := Load(map[string]string{"brief.ready_after": "7am"}); err == nil {
		t.Fatal("brief.ready_after=7am should fail to load")
	}
}

// TestSaveRefusesNewerSchema: Save must migrate the existing file the way
// Load does, never re-stamp a newer file with an older schema.
func TestSaveRefusesNewerSchema(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WATER_HOME", home)
	orig := []byte("schema: 99\nvoice:\n  provider: noop\n")
	os.WriteFile(Path(), orig, 0o600)
	if err := Save(map[string]string{"voice.model": "x"}); err == nil {
		t.Fatal("Save over a newer-schema file should fail")
	}
	if got, _ := os.ReadFile(Path()); string(got) != string(orig) {
		t.Fatalf("file changed:\n%s", got)
	}
}

// TestSaveUnreadableFileDoesNotTruncate: a read error other than not-exist
// must abort Save rather than start from an empty map.
func TestSaveUnreadableFileDoesNotTruncate(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read a 0200 file")
	}
	home := t.TempDir()
	t.Setenv("WATER_HOME", home)
	if err := Save(map[string]string{"backend.preferred": "api"}); err != nil {
		t.Fatal(err)
	}
	os.Chmod(Path(), 0o200)
	t.Cleanup(func() { os.Chmod(Path(), 0o600) })
	if err := Save(map[string]string{"voice.provider": "noop"}); err == nil {
		t.Fatal("Save over an unreadable file should fail")
	}
	os.Chmod(Path(), 0o600)
	r, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Backend.Preferred != "api" {
		t.Fatalf("existing key lost: backend.preferred = %q", r.Backend.Preferred)
	}
}

// TestSaveKeepsStringValuesVerbatim: string-typed keys keep exactly what was
// entered even when the text looks like a number or a boolean.
func TestSaveKeepsStringValuesVerbatim(t *testing.T) {
	t.Setenv("WATER_HOME", t.TempDir())
	in := map[string]string{"voice.ceo_voice": "t", "api.model": "0123", "voice.model": "true", "backend.allow_metered": "true", "sync.interval_minutes": "15"}
	if err := Save(in); err != nil {
		t.Fatal(err)
	}
	r, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Voice.CEOVoice != "t" || r.API.Model != "0123" || r.Voice.Model != "true" {
		t.Fatalf("string values mangled: ceo_voice=%q api.model=%q voice.model=%q", r.Voice.CEOVoice, r.API.Model, r.Voice.Model)
	}
	if !r.Backend.AllowMetered || r.Sync.IntervalMinutes != 15 {
		t.Fatalf("typed values lost: %+v", r.Flat())
	}
}

// TestEveryKeyRoundTrips ties the three hand-written key lists together:
// defaults() (Keys), apply() and Flat(). A key missing from apply() never
// reaches the typed Config; one missing from Flat() shows up blank in
// `water config` and "unknown" in `water config get`.
func TestEveryKeyRoundTrips(t *testing.T) {
	t.Setenv("WATER_HOME", t.TempDir())
	r, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	flat := r.Flat()
	keys := Keys()
	if len(flat) != len(keys) {
		t.Fatalf("Flat has %d keys, Keys has %d", len(flat), len(keys))
	}
	for _, k := range keys {
		if _, ok := flat[k]; !ok {
			t.Fatalf("Flat is missing key %q", k)
		}
	}

	flags := map[string]string{}
	for i, k := range keys {
		switch {
		case k == "schema":
			continue
		case boolKeys[k]:
			flags[k] = "true"
		case strings.HasPrefix(k, "sync."):
			flags[k] = strconv.Itoa(1000 + i)
		case k == "brief.ready_after":
			flags[k] = "05:4" + strconv.Itoa(i%10)
		default:
			flags[k] = "v-" + k
		}
	}
	r, err = Load(flags)
	if err != nil {
		t.Fatal(err)
	}
	flat = r.Flat()
	for k, want := range flags {
		if k == "api.key" {
			want = mask(want)
		}
		if flat[k] != want {
			t.Errorf("%s: set %q, Flat returned %q (missing from apply or Flat?)", k, flags[k], flat[k])
		}
	}
}
