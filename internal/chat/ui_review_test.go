package chat

import (
	"context"
	"fmt"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"water/internal/agent"
	"water/internal/backend"
)

func uiReviewModel(t *testing.T, width, height int) *model {
	t.Helper()
	m := testModel(t)
	m.width, m.height = width, height
	if err := m.enterRole("ceo", ""); err != nil { t.Fatal(err) }
	m.showHero = false
	m.relayout()
	return m
}

func TestUIReviewCommandOutputAfterTurn(t *testing.T) {
	for _, cmd := range []string{"/help", "/status", "/model", "/backend", "/usage", "/attach list", "/resume"} {
		t.Run(cmd, func(t *testing.T) {
			m := uiReviewModel(t, 100, 30)
			if _, err := m.sess.Send(m.ctx, "first prompt"); err != nil { t.Fatal(err) }
			m.rebuildTranscript()
			res := Dispatch(m.ctx, m.sess, cmd)
			if res.Err != nil { t.Fatal(res.Err) }
			m.applyResult(res)
			output := strings.TrimSpace(strings.Split(res.Output, "\n")[0])
			got := ansi.Strip(strings.Join(m.lines, "\n"))
			if !strings.Contains(got, output) { t.Errorf("command %s returned %q but rendered transcript = %q", cmd, output, got) }
		})
	}
}

func TestUIReviewRetryAfterRateLimit(t *testing.T) {
	for _, previous := range []bool{false, true} {
		t.Run(fmt.Sprint(previous), func(t *testing.T) {
			m := uiReviewModel(t, 100, 30)
			if previous {
				if _, err := m.sess.Send(m.ctx, "previous successful prompt"); err != nil { t.Fatal(err) }
			}
			m.sess.Env.Backend.(*backend.Fake).FailWith = backend.ErrRateLimited
			const pending = "new prompt requiring retry"
			m.ed.SetValue(pending)
			m.updateChat(tea.KeyPressMsg{Code: tea.KeyEnter})
			turn, err := m.sess.Send(m.ctx, pending)
			if err == nil { t.Fatal("expected rate limit") }
			m.Update(turnMsg{turn: turn, err: err})
			res := Dispatch(m.ctx, m.sess, "/retry")
			history, historyOK := m.sess.HistoryPrev()
			t.Logf("composer=%q visiblePending=%v retry={action:%d arg:%q err:%v} upHistory=%q/%v", m.ed.Value(), strings.Contains(ansi.Strip(strings.Join(m.lines,"\n")), pending), res.Action, res.Arg, res.Err, history, historyOK)
			if res.Err != nil || res.Arg != pending { t.Errorf("retry does not recover latest failed request") }
			if err := m.sess.resume(m.sess.Slug); err != nil { t.Fatal(err) }
			if last, ok := m.sess.LastUser(); !ok || last != pending { t.Errorf("resume ignores persisted failed user turn: last=%q ok=%v", last, ok) }
		})
	}
}

func TestUIReviewPasteGeometry(t *testing.T) {
	m := uiReviewModel(t, 80, 24)
	m.Update(tea.PasteMsg{Content: "first line\nsecond line\nthird line"})
	view := m.View()
	t.Logf("editorRows=%d allocatedRows=%d cursor=%+v", m.ed.Rows(), m.regions.Input.H, view.Cursor)
	if m.regions.Input.H != 3 || !strings.Contains(ansi.Strip(view.Content), "third line") { t.Errorf("multiline paste is hidden until a later key causes relayout") }
	m.busy = true
	m.Update(tea.PasteMsg{Content: "pasted while busy"})
	t.Logf("busy paste accepted=%v", strings.Contains(m.ed.Value(), "pasted while busy"))
}

func TestUIReviewResizeGeometry(t *testing.T) {
	m := uiReviewModel(t, 100, 30)
	m.ed.SetValue(strings.Repeat("word ", 14))
	m.relayout()
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 20})
	t.Logf("after narrowing: editorRows=%d allocatedRows=%d", m.ed.Rows(), m.regions.Input.H)
	if m.regions.Input.H != min(3, m.ed.Rows()) { t.Errorf("layout uses editor row count from old width") }
}

func TestUIReviewTinyTerminal(t *testing.T) {
	m := uiReviewModel(t, 30, 6)
	v := m.View()
	rows := strings.Count(v.Content, "\n")+1
	t.Logf("terminalHeight=%d renderedRows=%d cursor=%+v", m.height, rows, v.Cursor)
	if rows > m.height || (v.Cursor != nil && v.Cursor.Y >= m.height) { t.Errorf("layout extends beyond physical terminal") }
}

func TestUIReviewModelAndBackendDispatch(t *testing.T) {
	s, old := newSession(t, func(req backend.Request) string { return "old reply" })
	next := backend.NewFake("next-fake")
	s.SwitchBackend = func(string) (backend.Backend, string, error) { return next, "review", nil }
	for _, cmd := range []string{"/model local-review-model", "/backend next-fake"} {
		if res := Dispatch(context.Background(), s, cmd); res.Err != nil { t.Fatal(res.Err) }
	}
	if _, err := s.Send(context.Background(), "verify dispatch"); err != nil { t.Fatal(err) }
	if old.Calls() != 0 || next.Calls() != 1 || next.Requests()[0].Model != "local-review-model" { t.Fatalf("switch routing failed") }
	t.Logf("next backend receives model=%q; oldCalls=%d nextCalls=%d", next.Requests()[0].Model, old.Calls(), next.Calls())
}

func TestUIReviewInputCost(t *testing.T) {
	for _, n := range []int{100, 10000, 100000} {
		m := uiReviewModel(t, 100, 30)
		m.lines = make([]string, n)
		for i := range m.lines { m.lines[i] = strings.Repeat("x", 90) }
		m.relayout()
		m.ed.SetValue("draft")
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		for i := 0; i < 10; i++ { m.updateChat(tea.KeyPressMsg{Code: tea.KeyLeft}) }
		elapsed := time.Since(start)
		runtime.ReadMemStats(&after)
		t.Logf("transcriptRows=%d meanKeyLatency=%s allocatedBytesPerKey=%d", n, elapsed/10, (after.TotalAlloc-before.TotalAlloc)/10)
	}
}

// Compile-time check keeps this overlay confined to the real session API.
var _ agent.Env
