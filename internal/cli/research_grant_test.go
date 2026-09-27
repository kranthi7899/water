package cli

import (
	"testing"
	"time"

	"water"
	"water/internal/twins"
)

// TestResearchWebGrantedAtR: both CEO manifests (real and demo) grant
// research.web at level R with a rate cap of at most 20/h, never on the
// auto allowlist (it is a model call on the subscription and its output is
// untrusted), and both registries buildCEORegistry makes carry it at level
// R, External, with its "Searching the web" activity label.
func TestResearchWebGrantedAtR(t *testing.T) {
	for _, id := range []string{realTwinID, demoTwinID} {
		t.Run(id, func(t *testing.T) {
			m, err := twins.Load(water.TwinsFS(), id)
			if err != nil {
				t.Fatal(err)
			}
			f, ok := m.Function("research.web")
			if !ok || f.Level != twins.R {
				t.Fatalf("research.web = %+v, %v; want granted at R", f, ok)
			}
			if f.Rate == nil || f.Rate.Max <= 0 || f.Rate.Max > 20 || time.Duration(f.Rate.Per) < time.Hour {
				t.Fatalf("research.web rate = %+v, want at most 20 per hour", f.Rate)
			}
			if m.AutoAllowed("research.web") {
				t.Fatal("research.web is on the auto allowlist")
			}
			reg, err := buildCEORegistryModel(id, nil, "agent@example.com", "", "", m.ModelFor(twins.TierFast))
			if err != nil {
				t.Fatal(err)
			}
			_, spec, ok := reg.Lookup("research.web")
			if !ok || spec.Level != twins.R || !spec.External || spec.Activity != "Searching the web" {
				t.Fatalf("registry research.web = %+v, %v", spec, ok)
			}
			// The plain builder (validation-only callers) registers it too.
			reg, err = buildCEORegistry(id, nil, "", "", "")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, ok := reg.Lookup("research.web"); !ok {
				t.Fatal("buildCEORegistry lacks research.web")
			}
		})
	}
	if _, err := loadTwinManifest(water.TwinsFS(), realTwinID, "", "", ""); err != nil {
		t.Fatal(err)
	}
}
