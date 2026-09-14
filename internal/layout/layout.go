// Package layout is the deterministic region engine (Part 7.4). It computes
// geometry from the terminal size ONLY — a theme is not an input, which is
// what makes CHAT and INPUT geometry theme-invariant by construction rather
// than by discipline. A collision check asserts it anyway.
package layout

import (
	"errors"
	"fmt"
	"strings"
)

// Region is a rectangle in cells; Row/Col are zero-based.
type Region struct {
	Name string
	Row  int
	Col  int
	W    int
	H    int
}

// Bottom is the exclusive last row.
func (r Region) Bottom() int { return r.Row + r.H }

// Visible reports whether the region has any area.
func (r Region) Visible() bool { return r.W > 0 && r.H > 0 }

// Regions is the fixed grid.
type Regions struct {
	Header Region
	Hero   Region
	Chat   Region
	Input  Region
	Status Region
	Width  int
	Height int
}

// Spec are the inputs that are NOT theme: terminal size, whether the hero is
// requested (entry/picker), and the input's current row count. Hero art size
// is deliberately NOT an input: the engine allocates a fixed hero box for a
// given terminal size and the art is scaled into it, so two themes with
// different art get byte-identical CHAT/INPUT geometry.
type Spec struct {
	Width, Height int
	ShowHero      bool
	InputRows     int // 1..3
}

// Fixed rows.
const (
	HeaderRows = 3
	StatusRows = 1
	MinChat    = 4
	MinHero    = 4
	MaxHero    = 12
	MinWidth   = 40
)

// ErrTooSmall is returned when even HEADER+CHAT+INPUT+STATUS cannot fit.
var ErrTooSmall = errors.New("terminal too small")

// Compute lays out the regions. Rules (enforced here, not by a model):
//   - HERO is dropped entirely when it cannot fit at MinHero rows or when the
//     terminal is narrower than the art can be scaled into — never overlapped
//     or clipped into CHAT or INPUT.
//   - CHAT takes every row the fixed regions leave.
//   - INPUT is 1..3 rows directly above STATUS and is never moved by HERO.
func Compute(s Spec) (Regions, error) {
	if s.Width < 1 || s.Height < 1 {
		return Regions{}, ErrTooSmall
	}
	rows := s.InputRows
	if rows < 1 {
		rows = 1
	}
	if rows > 3 {
		rows = 3
	}
	fixed := HeaderRows + rows + StatusRows
	if s.Height < fixed+MinChat {
		// Shrink the header before failing: a 12-row terminal still gets a chat.
		if s.Height >= 1+rows+StatusRows+MinChat {
			r := Regions{Width: s.Width, Height: s.Height}
			r.Header = Region{"header", 0, 0, s.Width, 1}
			r.Status = Region{"status", s.Height - 1, 0, s.Width, StatusRows}
			r.Input = Region{"input", r.Status.Row - rows, 0, s.Width, rows}
			r.Chat = Region{"chat", 1, 0, s.Width, r.Input.Row - 1}
			return r, nil
		}
		return Regions{}, fmt.Errorf("%w: %dx%d (need at least %d rows)", ErrTooSmall, s.Width, s.Height, fixed+MinChat)
	}
	r := Regions{Width: s.Width, Height: s.Height}
	r.Header = Region{"header", 0, 0, s.Width, HeaderRows}
	r.Status = Region{"status", s.Height - StatusRows, 0, s.Width, StatusRows}
	r.Input = Region{"input", r.Status.Row - rows, 0, s.Width, rows}

	heroH := 0
	if s.ShowHero {
		avail := r.Input.Row - r.Header.Bottom() - MinChat
		if avail > MaxHero {
			avail = MaxHero
		}
		// Drop the hero entirely when it cannot have MinHero rows or the
		// terminal is too narrow for any art; never clip into CHAT or INPUT.
		if avail >= MinHero && s.Width >= MinWidth {
			heroH = avail
		}
	}
	if heroH > 0 {
		r.Hero = Region{"hero", r.Header.Bottom(), 0, s.Width, heroH}
		r.Chat = Region{"chat", r.Hero.Bottom(), 0, s.Width, r.Input.Row - r.Hero.Bottom()}
	} else {
		r.Chat = Region{"chat", r.Header.Bottom(), 0, s.Width, r.Input.Row - r.Header.Bottom()}
	}
	if err := r.Check(); err != nil {
		return Regions{}, err
	}
	return r, nil
}

// Check asserts no region overlaps another and CHAT/INPUT are intact.
func (r Regions) Check() error {
	regs := []Region{r.Header, r.Hero, r.Chat, r.Input, r.Status}
	for i := range regs {
		if !regs[i].Visible() {
			continue
		}
		if regs[i].Row < 0 || regs[i].Bottom() > r.Height || regs[i].W != r.Width {
			return fmt.Errorf("region %s out of bounds", regs[i].Name)
		}
		for j := range regs {
			if i == j || !regs[j].Visible() {
				continue
			}
			if regs[i].Row < regs[j].Bottom() && regs[j].Row < regs[i].Bottom() {
				return fmt.Errorf("region %s collides with %s", regs[i].Name, regs[j].Name)
			}
		}
	}
	if r.Chat.H < 1 || r.Input.H < 1 {
		return errors.New("chat or input has no rows")
	}
	if r.Input.Bottom() != r.Status.Row {
		return errors.New("input is not directly above status")
	}
	return nil
}

// ScaleArt scales hero art to fit w×h with nearest-neighbour sampling,
// preserving aspect ratio (never stretched). Returns rows padded/cropped to
// exactly h rows; each row is at most w cells wide, centred.
func ScaleArt(lines []string, w, h int) []string {
	if len(lines) == 0 || w <= 0 || h <= 0 {
		return nil
	}
	srcH := len(lines)
	srcW := 0
	for _, l := range lines {
		if n := len([]rune(l)); n > srcW {
			srcW = n
		}
	}
	scale := float64(h) / float64(srcH)
	if float64(srcW)*scale > float64(w) {
		scale = float64(w) / float64(srcW)
	}
	dstH := int(float64(srcH)*scale + 0.5)
	dstW := int(float64(srcW)*scale + 0.5)
	if dstH < 1 || dstW < 1 {
		return nil
	}
	out := make([]string, 0, h)
	pad := (w - dstW) / 2
	if pad < 0 {
		pad = 0
	}
	top := (h - dstH) / 2
	for i := 0; i < top; i++ {
		out = append(out, "")
	}
	for y := 0; y < dstH; y++ {
		sy := int(float64(y) / scale)
		if sy >= srcH {
			sy = srcH - 1
		}
		src := []rune(lines[sy])
		var sb strings.Builder
		sb.WriteString(strings.Repeat(" ", pad))
		for x := 0; x < dstW; x++ {
			sx := int(float64(x) / scale)
			if sx < len(src) {
				sb.WriteRune(src[sx])
			} else {
				sb.WriteByte(' ')
			}
		}
		out = append(out, sb.String())
	}
	for len(out) < h {
		out = append(out, "")
	}
	return out[:h]
}
