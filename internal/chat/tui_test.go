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
