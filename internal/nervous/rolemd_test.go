package nervous

import (
	"io/fs"
	"strings"
	"testing"

	water "water"
	"water/internal/nervous/render"
	"water/internal/runtime"
)

// TestRoleSystemGeneralSpecificBoundary pins Slice W's D1/D4a/D4d prompt
// contract on the real embedded twins/ceo/role.md, exactly as
// runtime.RoleSystem hands it to the main model: the general-knowledge
// license, the live-facts route (research.web), the spoken-email and
// draft-vs-send rules, a filled Environment, and no "on it" filler promise.
func TestRoleSystemGeneralSpecificBoundary(t *testing.T) {
	b, err := fs.ReadFile(water.TwinsFS(), "twins/ceo/role.md")
	if err != nil {
		t.Fatal(err)
	}
	style, err := render.LoadStyle(water.TwinsFS(), "ceo")
	if err != nil {
		t.Fatal(err)
	}
	sys := runtime.RoleSystem(runtime.Env{RoleMD: string(b), StyleBlock: style.PromptBlock()})
	lower := strings.ToLower(sys)

	for _, want := range []string{
		"## General knowledge and conversation",
		"Never say a question is\n  outside your scope",
		"research.web (the research__web tool)",
		"Company facts",
		"General knowledge",
		"Never record it",
		"## Email addresses heard by voice",
		"at the rate",
		"never `@therightgmail.com`",
		"Possible email\naddresses heard",
		"spelled out",
		"confirm_unusual_recipient: true",
		"## Drafts versus sends",
		"gmail.draft_for_review",
		"confirm send",
		"display.show",
		"Renaissance",
		"TODO(owner)",
	} {
		if !strings.Contains(sys, want) {
			t.Errorf("role system prompt is missing %q", want)
		}
	}
	// Slice W integration eval (2026-09-26): haiku hedged on strategy,
	// drafted in the same turn it showed the address, and answered "who's on
	// Halcyon" from the Environment list with no tool. Checked with line
	// wrapping collapsed.
	flat := strings.Join(strings.Fields(sys), " ")
	for _, want := range []string{
		"Never answer only with a question",
		"The read-back and the draft happen in separate turns",
		"call no draft or send tool; draft only after the CEO's yes arrives",
		"not a source to answer from",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("role system prompt is missing %q", want)
		}
	}
	for _, bad := range []string{
		`says "on it"`,
		"fill in for your company",
		"_(name, stage",
	} {
		if strings.Contains(lower, strings.ToLower(bad)) {
			t.Errorf("role system prompt still contains %q", bad)
		}
	}
}

// TestCeoDemoInheritsCeoRole: ceo-demo has no role.md of its own, so
// internal/cli's loadRoleMD falls back to twins/ceo/role.md and the demo
// gets the same D1/D4 rules with no separate copy to drift (W plan R1).
func TestCeoDemoInheritsCeoRole(t *testing.T) {
	if _, err := fs.Stat(water.TwinsFS(), "twins/ceo-demo/role.md"); err == nil {
		t.Fatal("twins/ceo-demo/role.md exists; it would shadow the ceo role's general/specific rules")
	}
}
