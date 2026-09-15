package chat

import (
	"context"
	"os"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"water/internal/agent"
	"water/internal/backend"
	"water/internal/memory"
	"water/internal/persona"
	"water/internal/roles"
	"water/internal/session"
	themepkg "water/internal/theme"
	"water/internal/tools"
)

func testModel(t *testing.T) *model {
	t.Helper()
	mem, _ := memory.NewMarkdown(memory.Options{Root: t.TempDir()})
	reg, err := roles.Load(persona.NewEmbedded(testTree()), mem)
	if err != nil {
		t.Fatal(err)
	}
	fake := backend.NewFake("fake-sub")
	fake.Reply = func(req backend.Request) string { return "reply" }
	root := t.TempDir()
	m := &model{opts: Options{
		Registry: reg,
		Themes:   os.DirFS("../.."),
		EnvFor: func(r *roles.Role) (agent.Env, BackendInfo, error) {
			return agent.Env{Backend: fake}, BackendInfo{Name: "fake-sub", Reason: "test", Auth: "subscription"}, nil
		},
		StoreFor: func(r *roles.Role) *session.Store { return session.New(root, r.Slug) },
	}, ctx: context.Background(), themes: map[string]*themepkg.Theme{}}
	return m
}

func TestTabCompletesSlashCommand(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 100, 30
	if err := m.enterRole("ceo", ""); err != nil {
		t.Fatal(err)
	}
	m.relayout()
	for _, r := range "/comp" {
		m.updateChat(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	if got := Complete(m.ed.Value()); len(got) != 1 || got[0].Name != "compact" {
		t.Fatalf("complete on %q: %+v", m.ed.Value(), got)
	}
	m.updateChat(tea.KeyPressMsg{Code: tea.KeyTab})
	if v := m.ed.Value(); v != "/compact " {
		t.Fatalf("after tab: %q", v)
	}
	// The dropdown is drawn inside the CHAT region; INPUT geometry unchanged.
	before := m.regions
	m.ed.Reset()
	for _, r := range "/co" {
		m.updateChat(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	v := m.View()
	if !strings.Contains(v.Content, "/copy [N]") || !strings.Contains(v.Content, "tab completes") {
		t.Fatal("dropdown not rendered")
	}
	if m.regions.Input != before.Input || m.regions.Chat != before.Chat {
		t.Fatal("dropdown changed geometry")
	}
	// Exit summary carries the resume command once a session exists.
	if s := m.exitSummary().String(); !strings.HasPrefix(s, "exited water · ceo") {
		t.Fatalf("summary: %q", s)
	}
}

// TestCompletedTurnClearsComposer guards the async completion path. A sent
// prompt belongs in the transcript; it must never remain in the composer as a
// misleading second, editable copy after the agent has replied.
func TestCompletedTurnClearsComposer(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 100, 30
	if err := m.enterRole("ceo", ""); err != nil {
		t.Fatal(err)
	}
	m.relayout()
	m.ed.SetValue("write a brief")
	m.busy = true
	updated, _ := m.Update(turnMsg{turn: Turn{Backend: "fake-sub"}})
	got := updated.(*model)
	if got.busy || got.ed.Value() != "" {
		t.Fatalf("completion left composer state: busy=%v value=%q", got.busy, got.ed.Value())
	}
}

func TestInteractiveWorkspaceApprovalIsScopedToActiveRole(t *testing.T) {
	m := testModel(t)
	root := t.TempDir()
	m.opts.WorkspacePolicy = func(r *roles.Role) *tools.Policy {
		return tools.InteractiveWorkspacePolicy(r.Slug, r.RoleID, root, t.TempDir(), "test-socket")
	}
	m.width, m.height = 100, 30
	if err := m.enterRole("ceo", ""); err != nil {
		t.Fatal(err)
	}
	pol := m.sess.Env.RoleTools["ceo"]
	if pol == nil || len(pol.ToolNames()) != 4 || m.sess.Env.RoleTools["cto"] != nil {
		t.Fatalf("interactive scope leaked or missing: %+v", m.sess.Env.RoleTools)
	}
	m.busy = true
	m.mode = modeApproval
	m.approval = tools.NewPendingApproval(tools.ApprovalRequest{Role: "ceo", Tool: tools.ToolWriteFile, Args: map[string]any{"path": root + "/brief.md"}})
	if view := m.View().Content; !strings.Contains(view, "ACTION APPROVAL") || !strings.Contains(view, "allow once") {
		t.Fatalf("approval UI not rendered: %s", view)
	}
	updated, _ := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	got := updated.(*model)
	if got.mode != modeChat || got.approval != nil || !strings.Contains(got.status, "approved once") {
		t.Fatalf("approval acceptance: mode=%v approval=%+v status=%q", got.mode, got.approval, got.status)
	}
}

func TestApprovalSummaryShowsEffectWithoutDumpingContent(t *testing.T) {
	got := approvalSummary(tools.ApprovalRequest{Tool: tools.ToolWriteFile, Args: map[string]any{"path": "/work/brief.md", "content": strings.Repeat("x", 200)}})
	if !strings.Contains(got, "/work/brief.md") || !strings.Contains(got, "200 bytes") || len(got) > 210 {
		t.Fatalf("write summary = %q", got)
	}
	if got := approvalSummary(tools.ApprovalRequest{Tool: tools.ToolRun, Args: map[string]any{"command": "cupsfilter deck.html > deck.pdf"}}); got != "run cupsfilter deck.html > deck.pdf" {
		t.Fatalf("run summary = %q", got)
	}
}
