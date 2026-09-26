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
// memory blocks, per-role voices, and (Tier 1's retirement) router.tier1.*
// must still load, silently dropping them.
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
		"voice:\n  ceo_voice: marin\n  coo_voice: cedar\n  cto_voice: ash\n  design_voice: coral\n" +
		"router:\n  tier1:\n    enabled: true\n    server_bin: llama-server\n"
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
		"decider.provider":      "jev",
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

// TestDeciderProviderDefaultsToNoneAndRejectsAnythingElse is R-24's own
// config test: decider.provider defaults to "none" (internal/decider.Null),
// round-trips when explicitly set to "none", and apply() refuses any other
// value with a clear error naming the key — there is no Jev or other
// adapter to select yet (docs/slices/R.md §15/§17).
func TestDeciderProviderDefaultsToNoneAndRejectsAnythingElse(t *testing.T) {
	t.Setenv("WATER_HOME", t.TempDir())
	r, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.Decider.Provider != "none" {
		t.Fatalf("decider.provider default = %q, want %q", r.Decider.Provider, "none")
	}

	r, err = Load(map[string]string{"decider.provider": "none"})
	if err != nil {
		t.Fatalf("decider.provider=none should load: %v", err)
	}
	if r.Decider.Provider != "none" || r.Flat()["decider.provider"] != "none" {
		t.Fatalf("decider.provider did not round trip: %+v", r.Flat())
	}

	if _, err := Load(map[string]string{"decider.provider": "jev"}); err == nil {
		t.Fatal("decider.provider=jev should fail to load")
	} else if !strings.Contains(err.Error(), `decider.provider: only "none" is supported`) {
		t.Fatalf("decider.provider=jev error = %q, want it to say only \"none\" is supported", err.Error())
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
		case k == "router.ack_ms":
			// apply() rejects any value >= 300; the generic intKeys branch
			// below would otherwise pick 1000+i, which always fails.
			flags[k] = "290"
		case intKeys[k]:
			flags[k] = strconv.Itoa(1000 + i)
		case k == "brief.ready_after":
			flags[k] = "05:4" + strconv.Itoa(i%10)
		case k == "decider.provider":
			// apply() rejects anything but "none" (only Null is supported
			// today), so this key can't take an arbitrary "v-<key>" value
			// like the other string keys below.
			flags[k] = "none"
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

// TestRouterConfigDefaults is R-25's own defaults check: every key
// docs/slices/R.md Design §17's table adds in this task defaults exactly as
// documented, and the numeric ones match the code defaults
// internal/nervous.DefaultConfig()/DefaultBreakerConfig() already use, so
// the daemon's real wiring (internal/cli/cmd_daemon.go) behaves identically
// whether or not the owner has ever touched config.yaml.
func TestRouterConfigDefaults(t *testing.T) {
	t.Setenv("WATER_HOME", t.TempDir())
	r, err := Load(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Router.Tier0.Enabled {
		t.Error("router.tier0.enabled default should be true")
	}
	if r.Router.Tier0.TimeoutMS != 150 {
		t.Errorf("router.tier0.timeout_ms default = %d, want 150", r.Router.Tier0.TimeoutMS)
	}
	if !r.Router.Main.Enabled {
		t.Error("router.main.enabled default should be true")
	}
	if r.Router.Breaker.Failures != 5 {
		t.Errorf("router.breaker.failures default = %d, want 5", r.Router.Breaker.Failures)
	}
	if r.Router.Breaker.CooldownSeconds != 60 {
		t.Errorf("router.breaker.cooldown_seconds default = %d, want 60", r.Router.Breaker.CooldownSeconds)
	}
	if r.Router.Breaker.MaxMissRatePct != 20 {
		t.Errorf("router.breaker.max_miss_rate_pct default = %d, want 20", r.Router.Breaker.MaxMissRatePct)
	}
	if r.Router.PossibleMissWindowSeconds != 60 {
		t.Errorf("router.possible_miss_window_seconds default = %d, want 60", r.Router.PossibleMissWindowSeconds)
	}
	if r.Router.LogRetentionDays != 90 {
		t.Errorf("router.log_retention_days default = %d, want 90", r.Router.LogRetentionDays)
	}
	if r.Router.AckMS != 250 {
		t.Errorf("router.ack_ms default = %d, want 250", r.Router.AckMS)
	}
	if !r.Router.Speculation.Enabled {
		t.Error("router.speculation.enabled default should be true")
	}
	if !r.Router.QuickTools.Enabled {
		t.Error("router.quick_tools.enabled default should be true")
	}
}

// TestRouterAckMSRejectsAtOrAbove300 is Design §17's own hard rule: the
// handoff acknowledgement must arrive well inside the 300ms budget R-12's
// tests lock in, so apply() refuses to even load a config that couldn't
// possibly meet it.
func TestRouterAckMSRejectsAtOrAbove300(t *testing.T) {
	t.Setenv("WATER_HOME", t.TempDir())
	for _, bad := range []string{"300", "301", "1000"} {
		if _, err := Load(map[string]string{"router.ack_ms": bad}); err == nil {
			t.Errorf("router.ack_ms=%s should fail to load", bad)
		} else if !strings.Contains(err.Error(), "router.ack_ms") {
			t.Errorf("router.ack_ms=%s error = %q, want it to name the key", bad, err.Error())
		}
	}
	for _, good := range []string{"0", "1", "100", "299"} {
		r, err := Load(map[string]string{"router.ack_ms": good})
		if err != nil {
			t.Errorf("router.ack_ms=%s should load: %v", good, err)
			continue
		}
		if strconv.Itoa(r.Router.AckMS) != good {
			t.Errorf("router.ack_ms=%s did not round trip: got %d", good, r.Router.AckMS)
		}
	}
	// Save must refuse it too, exactly like the other rejected values in
	// TestSaveRejectsInvalidValues.
	if err := Save(map[string]string{"router.ack_ms": "300"}); err == nil {
		t.Error("Save(router.ack_ms=300) should fail")
	}
}

// TestRouterIntBoolCoercion exercises int/bool coercion for a
// representative new key of each kind, the same style
// TestSaveRejectsInvalidValues already uses for the pre-existing keys.
func TestRouterIntBoolCoercion(t *testing.T) {
	t.Setenv("WATER_HOME", t.TempDir())
	if _, err := Load(map[string]string{"router.tier0.timeout_ms": "soon"}); err == nil {
		t.Fatal("router.tier0.timeout_ms=soon should fail to load")
	}
	if _, err := Load(map[string]string{"router.speculation.enabled": "sure"}); err == nil {
		t.Fatal("router.speculation.enabled=sure should fail to load")
	}
	r, err := Load(map[string]string{"router.tier0.timeout_ms": "75", "router.speculation.enabled": "false"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Router.Tier0.TimeoutMS != 75 || r.Router.Speculation.Enabled {
		t.Fatalf("coercion lost values: %+v", r.Flat())
	}
}
