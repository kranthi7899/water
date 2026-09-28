package cli

import (
	"testing"
	"time"

	"water"
	"water/internal/twins"
)

// TestDisplayShowGrantedAtR: both CEO manifests (real and demo) grant
// display.show at level R with a rate cap, never on the auto allowlist,
// and both registries buildCEORegistry makes carry it at level R with its
// "Showing you this" activity label.
func TestDisplayShowGrantedAtR(t *testing.T) {
	for _, id := range []string{"ceo", demoTwinID} {
		t.Run(id, func(t *testing.T) {
			m, err := twins.Load(water.TwinsFS(), id)
			if err != nil {
				t.Fatal(err)
			}
			f, ok := m.Function("display.show")
			if !ok || f.Level != twins.R {
				t.Fatalf("display.show = %+v, %v; want granted at R", f, ok)
			}
			if f.Rate == nil || f.Rate.Max <= 0 || f.Rate.Max > 60 || time.Duration(f.Rate.Per) <= 0 {
				t.Fatalf("display.show rate = %+v, want a modest cap", f.Rate)
			}
			if m.AutoAllowed("display.show") {
				t.Fatal("display.show is on the auto allowlist")
			}
			reg, err := buildCEORegistry(id, nil, "agent@example.com", "", "")
			if err != nil {
				t.Fatal(err)
			}
			_, spec, ok := reg.Lookup("display.show")
			if !ok || spec.Level != twins.R || spec.Activity != "Showing you this" {
				t.Fatalf("registry display.show = %+v, %v", spec, ok)
			}
		})
	}
}
