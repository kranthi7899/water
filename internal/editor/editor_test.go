package editor

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func typeString(e *TextArea, s string) {
	for _, g := range Graphemes(s) {
		e.Handle(tea.KeyPressMsg{Text: g, Code: []rune(g)[0]})
	}
}

// TestInputWrapVisible — type a string longer than the terminal width; it
// must wrap to a new visual row rather than truncating with an ellipsis, and
// the caret must stay visible (on the last row, at the correct cell).
func TestInputWrapVisible(t *testing.T) {
	e := New()
	e.SetWidth(30) // 28 usable after the "> " prompt
	e.Focus()
	long := strings.Repeat("abcde ", 10) // 60 cells
	typeString(e, long)
	if e.Rows() < 3 {
		t.Fatalf("expected wrapping into >=3 rows, got %d", e.Rows())
	}
	view := e.View()
	if strings.Contains(view, "…") || strings.Contains(view, "...") {
		t.Fatalf("view truncated with ellipsis:\n%s", view)
	}
	lines := strings.Split(strings.TrimRight(view, "\n"), "\n")
	if len(lines) < 3 {
		t.Fatalf("view has %d rows, want the wrapped rows visible:\n%s", len(lines), view)
	}
	// Every typed character must be present somewhere in the view.
	joined := strings.Join(lines, "")
	if strings.Count(joined, "abcde") != 10 {
		t.Fatalf("typed text not fully visible (%d/10 groups):\n%s", strings.Count(joined, "abcde"), view)
	}
	// Caret: on the last visual row, never past the width.
	if e.CursorRow() != e.Rows()-1 {
		t.Fatalf("cursor row %d, want last row %d", e.CursorRow(), e.Rows()-1)
	}
	if c := e.CursorCell(); c < 0 || c > 28 {
		t.Fatalf("cursor cell %d out of range", c)
	}
	if cur := e.Cursor(); cur == nil || cur.Y < 0 || cur.Y >= 3 {
		t.Fatalf("real cursor not within the input rows: %+v", cur)
	}
}

// TestInputWideCursor — type CJK and emoji; the cursor column equals the
// summed cell width, not the rune count.
func TestInputWideCursor(t *testing.T) {
	e := New()
	e.SetWidth(60)
	e.Focus()
	s := "日本語😀ab" // 2+2+2+2+1+1 = 10 cells, 6 runes/clusters
	typeString(e, s)
	if got, want := e.CursorCell(), CellWidth(s); got != want {
		t.Fatalf("cursor cell %d, want %d (rune count would be %d)", got, want, len([]rune(s)))
	}
	if CellWidth(s) != 10 || len([]rune(s)) != 6 {
		t.Fatalf("width assumptions: cells=%d runes=%d", CellWidth(s), len([]rune(s)))
	}
	// Left by one grapheme cluster moves back one cell width, not one byte/rune.
	e.Handle(tea.KeyPressMsg{Code: tea.KeyLeft})
	if got := e.CursorCell(); got != 9 {
		t.Fatalf("after left: cell %d, want 9", got)
	}
	e.Handle(tea.KeyPressMsg{Code: tea.KeyLeft})
	e.Handle(tea.KeyPressMsg{Code: tea.KeyLeft}) // over the emoji
	if got := e.CursorCell(); got != 6 {
		t.Fatalf("after 3 lefts: cell %d, want 6", got)
	}
	// A family emoji with ZWJ is one cluster, two cells.
	fam := "👨‍👩‍👧"
	if Graphemes(fam)[0] != fam || len(Graphemes(fam)) != 1 {
		t.Fatalf("ZWJ sequence should be one cluster")
	}
	// The widget reserves one cell after the last character for the caret,
	// so 30 CJK characters (60 cells) at width 20 fill three rows and the
	// caret lands on a fourth; 29 characters fit in three.
	if got := VisualRows(strings.Repeat("日", 30), 20); got != 4 {
		t.Fatalf("30 CJK chars at width 20: got %d rows, want 4 (widget rule)", got)
	}
	if got := VisualRows(strings.Repeat("日", 29), 20); got != 3 {
		t.Fatalf("29 CJK chars at width 20: got %d rows, want 3", got)
	}
	if got := VisualRows(strings.Repeat("a", 45), 20); got != 3 {
		t.Fatalf("45 ascii at width 20: got %d rows, want 3", got)
	}
}

func TestKeybindings(t *testing.T) {
	e := New()
	e.SetWidth(40)
	e.Focus()
	typeString(e, "one two")
	if act, _ := e.Handle(tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}); act != ActNone || !strings.Contains(e.Value(), "\n") {
		t.Fatalf("ctrl+j should insert newline: %q", e.Value())
	}
	typeString(e, "three")
	if act, _ := e.Handle(tea.KeyPressMsg{Code: tea.KeyEnter}); act != ActSubmit {
		t.Fatal("enter should submit")
	}
	// Ctrl+W deletes the previous word.
	e.Handle(tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
	if strings.HasSuffix(e.Value(), "three") {
		t.Fatalf("ctrl+w did not delete word: %q", e.Value())
	}
	// Ctrl+U deletes to line start.
	typeString(e, "xyz")
	e.Handle(tea.KeyPressMsg{Code: 'u', Mod: tea.ModCtrl})
	if strings.HasSuffix(e.Value(), "xyz") {
		t.Fatalf("ctrl+u did not clear line: %q", e.Value())
	}
	// History only when empty.
	e.Reset()
	if act, _ := e.Handle(tea.KeyPressMsg{Code: tea.KeyUp}); act != ActHistoryPrev {
		t.Fatal("up on empty buffer should recall history")
	}
	typeString(e, "text")
	if act, _ := e.Handle(tea.KeyPressMsg{Code: tea.KeyUp}); act != ActNone {
		t.Fatal("up with text should navigate, not recall history")
	}
	// Shift+Enter hint only after detection.
	if strings.Contains(e.Hint(), "shift+enter") {
		t.Fatal("shift+enter advertised before detection")
	}
	e.SetKittyDetected(true)
	if !strings.Contains(e.Hint(), "shift+enter") {
		t.Fatal("shift+enter should be advertised after detection")
	}
	if act, _ := e.Handle(tea.KeyPressMsg{Code: 'x', Mod: tea.ModCtrl}); act != ActOpenEditor {
		t.Fatal("ctrl+x opens $EDITOR")
	}
}
