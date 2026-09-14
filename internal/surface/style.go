package surface

import (
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Visual identity: monochrome, high-contrast, etched line-art register.
// Near-mono palette with exactly one accent. lipgloss honours NO_COLOR.
var (
	accent = lipgloss.AdaptiveColor{Light: "#8a5a00", Dark: "#d9a441"}
	dim    = lipgloss.AdaptiveColor{Light: "#666666", Dark: "#8a8a8a"}
	ink    = lipgloss.AdaptiveColor{Light: "#111111", Dark: "#e6e6e6"}

	StyleAccent = lipgloss.NewStyle().Foreground(accent)
	StyleDim    = lipgloss.NewStyle().Foreground(dim)
	StyleInk    = lipgloss.NewStyle().Foreground(ink)
	StyleBold   = lipgloss.NewStyle().Foreground(ink).Bold(true)
	StyleRule   = lipgloss.NewStyle().Foreground(dim)
	StyleOK     = lipgloss.NewStyle().Foreground(ink)
	StyleWarn   = lipgloss.NewStyle().Foreground(accent).Bold(true)
	StyleErr    = lipgloss.NewStyle().Foreground(ink).Bold(true).Underline(true)
)

// Banner is the etched ASCII mark. Terse, oracular, no chipper copy.
const Banner = `
        .    *    .
     ·  \   |   /  ·
   -- ·  \  |  /  · --
        ·-(( ))-·
   -- ·  /  |  \  · --
     ·  /   |   \  ·
        '    *    '
      w  a  t  e  r
`

// Rule returns a horizontal rule sized to the terminal (max 72).
func Rule() string {
	return StyleRule.Render(strings.Repeat("─", 60))
}

// ColorEnabled reports whether output is styled.
func ColorEnabled() bool {
	if _, no := os.LookupEnv("NO_COLOR"); no {
		return false
	}
	return lipgloss.ColorProfile() != 0
}
