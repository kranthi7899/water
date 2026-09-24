// Package nervous is the front door every channel (CLI, hotkey pop-up,
// voice, meeting mode) calls: the sous chef's cascade (Tier 0, and later
// Tier 1) tries to answer instantly and with no model call, and only then
// hands off to the head chef (Claude, on the warm session). This file holds
// the minimal shared types later tasks build on; the full Tier interface,
// Outcome and the Nervous facade itself are R-12's job.
package nervous

// TierID names which tier answered or is being tried. Only "t0" exists in
// this codebase today; "t1" and "main" are added by later tasks.
type TierID string

const (
	TierT0 TierID = "t0"
)

// Owner names who currently owns a turn's answer. Mirrors
// internal/nervous/turn.Owner, duplicated here (rather than imported) so
// this package's minimal placeholder types don't force an early dependency
// on the turn package before R-12 wires the two together.
type Owner string

const (
	OwnerNone  Owner = ""
	OwnerQuick Owner = "quick"
	OwnerMain  Owner = "main"
)

// wordSet turns a slice of words (as loaded from an intent registry's
// Shared) into the set tmpl.Normalize and the deny/escalate-word checks
// need. Shared exposes only slices (it's a plain YAML-decoded struct), so
// every caller in this package that needs a set builds one with this
// instead of duplicating the map-building loop.
func wordSet(words []string) map[string]bool {
	if len(words) == 0 {
		return nil
	}
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}
