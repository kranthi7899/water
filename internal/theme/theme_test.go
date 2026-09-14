package theme

import (
	"os"
	"strings"
	"testing"
)

// TestThemeDegrades — a 16-colour terminal (and a no-colour one) renders every
// theme without error, and the degraded SGR never contains truecolor params.
func TestThemeDegrades(t *testing.T) {
	fsys := os.DirFS("../..")
	names := Names(fsys)
	if len(names) < 5 {
		t.Fatalf("themes: %v", names)
	}
	for _, name := range names {
		th, err := Load(fsys, name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, p := range []Profile{None, ANSI16, ANSI256, TrueColor} {
			st := Styler{Profile: p}
			for _, hex := range []string{th.Palette.Background, th.Palette.Foreground, th.Palette.Muted, th.Palette.Accent} {
				out := st.Fg(hex, "x")
				switch p {
				case None:
					if out != "x" {
						t.Fatalf("%s/none: %q", name, out)
					}
				case ANSI16:
					if strings.Contains(out, "38;2;") || strings.Contains(out, "38;5;") && sgrIndex(out) > 15 {
						t.Fatalf("%s/16: %q leaks beyond 16 colours", name, out)
					}
				case ANSI256:
					if strings.Contains(out, "38;2;") {
						t.Fatalf("%s/256: %q leaks truecolor", name, out)
					}
				}
			}
		}
		if th.Hero.Height() < 4 || th.Hero.Width() < 20 {
			t.Fatalf("%s hero too small: %dx%d", name, th.Hero.Width(), th.Hero.Height())
		}
	}
	// ForRole falls back to the base for an unknown role.
	th, err := ForRole(fsys, "fifth-role")
	if err != nil || th.Name != "water-base" {
		t.Fatalf("%v %v", th, err)
	}
	if DetectProfile(func(k string) string { return map[string]string{"NO_COLOR": "1", "TERM": "xterm-256color"}[k] }) != None {
		t.Fatal("NO_COLOR must win")
	}
	if DetectProfile(func(k string) string { return map[string]string{"COLORTERM": "truecolor", "TERM": "xterm"}[k] }) != TrueColor {
		t.Fatal("COLORTERM=truecolor")
	}
	if DetectProfile(func(k string) string { return map[string]string{"TERM": "vt100"}[k] }) != ANSI16 {
		t.Fatal("unknown terminal must degrade to 16")
	}
}

func sgrIndex(s string) int {
	i := strings.Index(s, "38;5;")
	if i < 0 {
		return 0
	}
	n := 0
	for _, c := range s[i+5:] {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}
