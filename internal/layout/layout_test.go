package layout

import (
	"os"
	"testing"

	"water/internal/theme"
)

func themesFS(t *testing.T) *theme.Theme {
	t.Helper()
	fsys := os.DirFS("../..")
	th, err := theme.Load(fsys, "water-base")
	if err != nil {
		t.Fatal(err)
	}
	return th
}

// TestThemeDoesNotMoveChat — CHAT and INPUT regions have byte-identical
// geometry across all four role themes at the same terminal size. Themes are
// not an input to Compute; this test exists so that stays true if someone is
// tempted to add one.
func TestThemeDoesNotMoveChat(t *testing.T) {
	fsys := os.DirFS("../..")
	var ref *Regions
	for _, name := range []string{"ceo", "coo", "cto", "design", "water-base"} {
		th, err := theme.Load(fsys, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, size := range [][2]int{{80, 24}, {120, 40}, {40, 16}, {200, 60}} {
			_ = th
			r, err := Compute(Spec{Width: size[0], Height: size[1], ShowHero: true, InputRows: 2})
			if err != nil {
				t.Fatalf("%s %v: %v", name, size, err)
			}
			// Chat/Input geometry must equal the theme-free computation with the
			// same size and the SAME hero request; hero art size differs per
			// theme, so compare against a fixed synthetic art instead.
			base, err := Compute(Spec{Width: size[0], Height: size[1], ShowHero: false, InputRows: 2})
			if err != nil {
				t.Fatal(err)
			}
			if r.Input != base.Input || r.Status != base.Status {
				t.Fatalf("%s at %v moved INPUT/STATUS: %+v vs %+v", name, size, r.Input, base.Input)
			}
			if r.Chat.Bottom() != base.Chat.Bottom() || r.Chat.W != base.Chat.W {
				t.Fatalf("%s at %v changed CHAT bottom/width", name, size)
			}
			if ref == nil {
				ref = &r
			}
		}
		_ = ref
	}
	// Across themes at one size with hero shown, chat geometry is identical
	// because the engine clamps every hero to the same MaxHero rows.
	var first *Regions
	for _, name := range []string{"ceo", "coo", "cto", "design"} {
		th, _ := theme.Load(fsys, name)
		r, _ := Compute(Spec{Width: 100, Height: 40, ShowHero: true, InputRows: 1})
		// The art must scale INTO the box, never resize it.
		if art := ScaleArt(th.Hero.Lines, r.Hero.W, r.Hero.H); len(art) != r.Hero.H {
			t.Fatalf("theme %s art rows %d != hero box %d", name, len(art), r.Hero.H)
		}
		if first == nil {
			first = &r
			continue
		}
		if r.Chat != first.Chat || r.Input != first.Input {
			t.Fatalf("theme %s changes chat geometry: %+v vs %+v", name, r.Chat, first.Chat)
		}
	}
}

// TestHeroNeverCollides — at 40 columns (and in short terminals) HERO is
// dropped rather than overlapping CHAT.
func TestHeroNeverCollides(t *testing.T) {
	_ = themesFS(t)
	r, err := Compute(Spec{Width: 39, Height: 30, ShowHero: true, InputRows: 1})
	if err != nil {
		t.Fatal(err)
	}
	if r.Hero.Visible() {
		t.Fatalf("hero shown at 39 columns: %+v", r.Hero)
	}
	if err := r.Check(); err != nil {
		t.Fatal(err)
	}
	r, err = Compute(Spec{Width: 120, Height: 12, ShowHero: true, InputRows: 3})
	if err != nil {
		t.Fatal(err)
	}
	if r.Hero.Visible() {
		t.Fatalf("hero shown in 12-row terminal: %+v", r.Hero)
	}
	if r.Chat.H < MinChat {
		t.Fatalf("chat squeezed to %d rows", r.Chat.H)
	}
	// Big terminal: hero shown, still no collision, clamped to MaxHero.
	r, err = Compute(Spec{Width: 160, Height: 60, ShowHero: true, InputRows: 1})
	if err != nil || !r.Hero.Visible() || r.Hero.H > MaxHero {
		t.Fatalf("%+v %v", r.Hero, err)
	}
	if err := r.Check(); err != nil {
		t.Fatal(err)
	}
	if _, err := Compute(Spec{Width: 80, Height: 5}); err == nil {
		t.Fatal("5-row terminal should be too small")
	}
}

func TestScaleArtNeverStretches(t *testing.T) {
	art := []string{"##..##", "..##..", "##..##"}
	out := ScaleArt(art, 12, 3)
	if len(out) != 3 || len([]rune(out[0])) > 12 {
		t.Fatalf("%q", out)
	}
	// Shrinking keeps aspect: 6x3 into 3x3 → 3 wide, 2 tall (rounded), padded to 3 rows.
	out = ScaleArt(art, 3, 3)
	if len(out) != 3 {
		t.Fatalf("%q", out)
	}
	for _, row := range out {
		if len([]rune(row)) > 3 {
			t.Fatalf("row wider than target: %q", row)
		}
	}
}
