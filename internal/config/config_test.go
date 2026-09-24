package config

import (
	"os"
	"path/filepath"
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
