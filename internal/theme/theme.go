// Package theme loads visual identities (Part 7). A theme declares palette,
// typography hints, and a hero motif. It never declares geometry: the layout
// engine computes WHERE, the renderer decides HOW, and the model is not
// involved in either (7.1).
//
// Invariant #7: a theme changes palette and ornament only. Input behaviour,
// command behaviour, and message layout are identical across themes.
package theme

import (
	"errors"
	"fmt"
	"image/color"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Theme is themes/<name>.yaml.
type Theme struct {
	Name       string     `yaml:"name"`
	Role       string     `yaml:"role"` // role slug this theme belongs to ("" = base)
	Palette    Palette    `yaml:"palette"`
	Typography Typography `yaml:"typography"`
	Borders    Borders    `yaml:"borders"`
	Imagery    Imagery    `yaml:"imagery"`
	Density    string     `yaml:"density"` // compact | comfortable
	Hero       Hero       `yaml:"hero"`
}

type Palette struct {
	Background string `yaml:"background"`
	Panel      string `yaml:"panel"`
	Foreground string `yaml:"foreground"`
	Muted      string `yaml:"muted"`
	Accent     string `yaml:"accent"`
	Signal     string `yaml:"signal"`
}

type Typography struct {
	Family string `yaml:"family"`
	Labels string `yaml:"labels"` // uppercase | normal
}

type Borders struct {
	Style  string `yaml:"style"` // ascii | rounded | thick | none
	Weight int    `yaml:"weight"`
}

type Imagery struct {
	Style     string `yaml:"style"`     // pixel_art
	Scaling   string `yaml:"scaling"`   // nearest
	Dithering string `yaml:"dithering"` // ordered | none
}

// Hero is the pixel-art motif rendered in the HERO region. Lines are plain
// text rows of equal width; the renderer scales nearest-neighbour and never
// stretches. Title is the large display word (e.g. WATER).
type Hero struct {
	Title string   `yaml:"title"`
	Lines []string `yaml:"lines"`
}

// Width returns the widest hero row in cells (ASCII art, so bytes == cells).
func (h Hero) Width() int {
	w := 0
	for _, l := range h.Lines {
		if n := len([]rune(l)); n > w {
			w = n
		}
	}
	return w
}

// Height returns the hero row count.
func (h Hero) Height() int { return len(h.Lines) }

// Load reads a theme file.
func Load(fsys fs.FS, name string) (*Theme, error) {
	b, err := fs.ReadFile(fsys, path.Join("themes", name+".yaml"))
	if err != nil {
		return nil, err
	}
	var wrap struct {
		Theme Theme `yaml:"theme"`
	}
	if err := yaml.Unmarshal(b, &wrap); err != nil {
		return nil, fmt.Errorf("theme %s: %w", name, err)
	}
	t := wrap.Theme
	if t.Name == "" {
		t.Name = name
	}
	if err := t.Validate(); err != nil {
		return nil, fmt.Errorf("theme %s: %w", name, err)
	}
	return &t, nil
}

// ForRole returns the role's theme, falling back to water-base.
func ForRole(fsys fs.FS, slug string) (*Theme, error) {
	if t, err := Load(fsys, slug); err == nil {
		return t, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return Load(fsys, "water-base")
}

// Names lists available theme names.
func Names(fsys fs.FS) []string {
	entries, err := fs.ReadDir(fsys, "themes")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".yaml") {
			out = append(out, strings.TrimSuffix(e.Name(), ".yaml"))
		}
	}
	sort.Strings(out)
	return out
}

// Validate checks every palette entry parses and hero rows are rectangular.
func (t *Theme) Validate() error {
	for k, v := range map[string]string{"background": t.Palette.Background, "foreground": t.Palette.Foreground, "muted": t.Palette.Muted, "accent": t.Palette.Accent} {
		if _, err := ParseHex(v); err != nil {
			return fmt.Errorf("palette.%s: %w", k, err)
		}
	}
	for _, opt := range []string{t.Palette.Panel, t.Palette.Signal} {
		if opt != "" {
			if _, err := ParseHex(opt); err != nil {
				return err
			}
		}
	}
	if len(t.Hero.Lines) > 0 {
		w := t.Hero.Width()
		for i, l := range t.Hero.Lines {
			if n := len([]rune(l)); n != w {
				return fmt.Errorf("hero.lines[%d] has width %d, want %d (rows must be rectangular)", i, n, w)
			}
		}
	}
	return nil
}

// RGB is a parsed palette colour.
type RGB struct{ R, G, B uint8 }

// ParseHex parses #rrggbb.
func ParseHex(s string) (RGB, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 {
		return RGB{}, fmt.Errorf("colour %q is not #rrggbb", s)
	}
	n, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return RGB{}, fmt.Errorf("colour %q is not hex", s)
	}
	return RGB{uint8(n >> 16), uint8(n >> 8), uint8(n)}, nil
}

// Color returns an image/color value.
func (c RGB) Color() color.Color { return color.RGBA{c.R, c.G, c.B, 0xff} }

// Hex renders #rrggbb.
func (c RGB) Hex() string { return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B) }

// Profile is the colour capability detected for the terminal (7.4: detect,
// never assume). Degradation order: TrueColor → ANSI256 → ANSI16 → None.
type Profile int

const (
	None Profile = iota
	ANSI16
	ANSI256
	TrueColor
)

func (p Profile) String() string {
	switch p {
	case TrueColor:
		return "truecolor"
	case ANSI256:
		return "256"
	case ANSI16:
		return "16"
	}
	return "none"
}

// DetectProfile inspects the environment the way terminals advertise
// capability. It is deliberately conservative: an unknown terminal gets 16
// colours, and NO_COLOR or TERM=dumb gets none.
func DetectProfile(env func(string) string) Profile {
	if env("NO_COLOR") != "" || env("TERM") == "dumb" || env("TERM") == "" {
		return None
	}
	ct := strings.ToLower(env("COLORTERM"))
	if ct == "truecolor" || ct == "24bit" {
		return TrueColor
	}
	term := strings.ToLower(env("TERM"))
	switch {
	case strings.Contains(term, "256color"), strings.Contains(term, "kitty"), strings.Contains(term, "ghostty"), strings.Contains(term, "wezterm"), strings.Contains(term, "alacritty"):
		return ANSI256
	case env("TERM_PROGRAM") == "iTerm.app", env("TERM_PROGRAM") == "vscode", env("TERM_PROGRAM") == "Apple_Terminal":
		return ANSI256
	}
	return ANSI16
}

// EnvProfile detects from the process environment.
func EnvProfile() Profile { return DetectProfile(os.Getenv) }

// Degrade maps an RGB colour to the nearest colour the profile can show and
// returns the SGR parameter string for it ("" for None). Foreground/background
// selection is the caller's.
func Degrade(c RGB, p Profile) string {
	switch p {
	case TrueColor:
		return fmt.Sprintf("2;%d;%d;%d", c.R, c.G, c.B)
	case ANSI256:
		return fmt.Sprintf("5;%d", to256(c))
	case ANSI16:
		return fmt.Sprintf("5;%d", to16(c))
	}
	return ""
}

func to256(c RGB) int {
	// Greyscale ramp when channels are close.
	if absDiff(c.R, c.G) < 10 && absDiff(c.G, c.B) < 10 {
		if c.R < 8 {
			return 16
		}
		if c.R > 248 {
			return 231
		}
		return 232 + int((float64(c.R)-8)/247*24)
	}
	q := func(v uint8) int { return int((float64(v) / 255) * 5) }
	return 16 + 36*q(c.R) + 6*q(c.G) + q(c.B)
}

func to16(c RGB) int {
	bright := int(c.R)+int(c.G)+int(c.B) > 384
	r, g, b := c.R > 96, c.G > 96, c.B > 96
	idx := 0
	if r {
		idx |= 1
	}
	if g {
		idx |= 2
	}
	if b {
		idx |= 4
	}
	if bright {
		idx += 8
	}
	return idx
}

func absDiff(a, b uint8) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// Styler renders text in a palette colour, degraded to the profile.
type Styler struct {
	Profile Profile
}

// Fg wraps s in a foreground SGR for hex, or returns s unchanged when the
// profile is None or the colour does not parse.
func (st Styler) Fg(hex, s string) string {
	c, err := ParseHex(hex)
	if err != nil || st.Profile == None {
		return s
	}
	return "\x1b[38;" + Degrade(c, st.Profile) + "m" + s + "\x1b[39m"
}

// Bg wraps s in a background SGR.
func (st Styler) Bg(hex, s string) string {
	c, err := ParseHex(hex)
	if err != nil || st.Profile == None {
		return s
	}
	return "\x1b[48;" + Degrade(c, st.Profile) + "m" + s + "\x1b[49m"
}

// Bold applies bold when any colour capability exists.
func (st Styler) Bold(s string) string {
	if st.Profile == None {
		return s
	}
	return "\x1b[1m" + s + "\x1b[22m"
}

// Label applies the typography rule (uppercase labels).
func (t *Theme) Label(s string) string {
	if strings.EqualFold(t.Typography.Labels, "uppercase") {
		return strings.ToUpper(s)
	}
	return s
}

// Rule returns a horizontal rule glyph for the border style.
func (t *Theme) Rule() string {
	switch t.Borders.Style {
	case "thick":
		return "━"
	case "rounded":
		return "─"
	case "none":
		return " "
	}
	return "-"
}
