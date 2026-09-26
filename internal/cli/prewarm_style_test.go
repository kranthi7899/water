package cli

import (
	"strings"
	"testing"

	"water/internal/gateway"
	"water/internal/nervous/render"
	"water/internal/runtime"
	"water/internal/twins"
)

// TestPrewarmSystemMatchesTurnSystem is the regression test for
// docs/slices/V.md D5: the style block reaches the main path's system
// prompt (gateway baseEnv) AND the prewarm request, byte-identically.
// WarmSession restarts its process whenever System differs, so a mismatch
// would throw the prewarmed process away on every first turn.
func TestPrewarmSystemMatchesTurnSystem(t *testing.T) {
	style := render.DefaultStyle()
	block := style.PromptBlock()
	if strings.TrimSpace(block) == "" {
		t.Fatal("default style has an empty prompt block")
	}
	m := &twins.Manifest{ID: "t"}
	const role = "You are the CEO's twin."
	d := gateway.New(gateway.Config{Manifest: m, RoleMD: role, StyleBlock: block, MaxChars: styleMaxChars(style)})
	turnSystem := d.SystemPrompt()
	if !strings.Contains(turnSystem, block) || !strings.HasPrefix(turnSystem, role) {
		t.Fatalf("turn system prompt lacks the role or style block:\n%s", turnSystem)
	}
	if turnSystem != runtime.RoleSystem(runtime.Env{RoleMD: role, StyleBlock: block}) {
		t.Fatal("SystemPrompt is not RoleSystem over the configured role and style")
	}

	// Before the daemon is wired, and after (how runDaemon builds it).
	p := &daemonPrewarmer{roleMD: role, manifest: m, styleBlock: block}
	if got := p.request().System; got != turnSystem {
		t.Fatalf("prewarm System (unwired) != turn System:\n%q\n%q", got, turnSystem)
	}
	p.d = d
	if got := p.request().System; got != turnSystem {
		t.Fatalf("prewarm System (wired) != turn System:\n%q\n%q", got, turnSystem)
	}
}

// TestStyleMaxCharsCoversEveryChannel: every turn channel's style.yaml cap
// reaches the channel hint.
func TestStyleMaxCharsCoversEveryChannel(t *testing.T) {
	got := styleMaxChars(render.DefaultStyle())
	for _, ch := range []runtime.Channel{runtime.ChannelCLI, runtime.ChannelTextBar, runtime.ChannelVoice} {
		if got[ch] <= 0 {
			t.Errorf("max chars for %s = %d", ch, got[ch])
		}
	}
	if got[runtime.ChannelVoice] != 280 {
		t.Errorf("voice cap = %d, want the default 280", got[runtime.ChannelVoice])
	}
}
