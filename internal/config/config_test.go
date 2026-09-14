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
	os.WriteFile(filepath.Join(home, "config.yaml"), []byte("schema: 1\nbackend:\n  preferred: codex-subscription\nmemory:\n  max_entries: 50\n"), 0o600)
	t.Setenv("WATER_MEMORY_MAX_ENTRIES", "75")

	r, err := Load(map[string]string{"backend.allow_metered": "true"})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][2]string{
		"backend.preferred":     {"codex-subscription", LayerFile},
		"memory.max_entries":    {"75", LayerEnv},
		"backend.allow_metered": {"true", LayerFlag},
		"orchestration.router":  {"ceo-fanout", LayerDefault},
	}
	flat := r.Flat()
	for k, want := range cases {
		if flat[k] != want[0] || r.Provenance[k] != want[1] {
			t.Errorf("%s = %q from %s; want %q from %s", k, flat[k], r.Provenance[k], want[0], want[1])
		}
	}
	if r.Memory.MaxEntries != 75 || !r.Backend.AllowMetered {
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
