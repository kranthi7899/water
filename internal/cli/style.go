package cli

import "github.com/charmbracelet/lipgloss"

// Terminal styling for the surviving commands (status, doctor, onboard,
// login, config). This used to be internal/surface, which also carried the
// multi-role Surface (run/node/message events for the orchestrator) and the
// council's ASCII banner; both went with the council in Phase 4. What is
// left is just a few named lipgloss styles, small enough to keep inline
// rather than as their own package. lipgloss honours NO_COLOR itself.
var (
	styleAccent = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#8a5a00", Dark: "#d9a441"})
	styleDim    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#666666", Dark: "#8a8a8a"})
	styleBold   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#111111", Dark: "#e6e6e6"}).Bold(true)
	styleOK     = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#111111", Dark: "#e6e6e6"})
	styleWarn   = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#8a5a00", Dark: "#d9a441"}).Bold(true)
	styleErr    = lipgloss.NewStyle().Foreground(lipgloss.AdaptiveColor{Light: "#111111", Dark: "#e6e6e6"}).Bold(true).Underline(true)
)
