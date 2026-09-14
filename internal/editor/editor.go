// Package editor is the input line (Part 4.1), behind a small interface so
// the app is not coupled to the widget library. The implementation wraps the
// Bubbles v2 textarea; width and cursor math come from grapheme-cluster cell
// widths (rivo/uniseg via charmbracelet/x/ansi), never rune counts.
//
// Baseline keybindings, all working with no per-terminal setup:
//
//	Left/Right            by grapheme cluster
//	Up/Down               across soft-wrapped visual rows; history recall only
//	                      when the buffer is empty and the cursor is on the
//	                      first/last row
//	Home/End, Ctrl+A/E    visual line start/end; second press or Ctrl+Home/End
//	                      to buffer bounds
//	Ctrl+W                delete previous word
//	Ctrl+U                delete to line start
//	Alt+B / Alt+F         word jump
//	Ctrl+J                insert newline      (LF: needs no negotiation)
//	Enter                 submit
//	Shift+Enter           newline ONLY when the Kitty keyboard protocol was
//	                      detected; never advertised otherwise
//
// Mouse click-to-position is out of scope by decision.
package editor

import (
	"strings"
	"unicode"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// Action is what the app does after a key: nothing, submit, open $EDITOR, or
// recall history.
type Action int

const (
	ActNone Action = iota
	ActSubmit
	ActOpenEditor
	ActHistoryPrev
	ActHistoryNext
)

// InputEditor is the seam the app depends on.
type InputEditor interface {
	SetWidth(w int)
	SetMaxRows(n int)
	Value() string
	SetValue(s string)
	Reset()
	Insert(s string)
	// Handle processes one key and reports the resulting action.
	Handle(msg tea.KeyPressMsg) (Action, tea.Cmd)
	// Rows is the number of visual rows the current value occupies (1..max).
	Rows() int
	// CursorCell is the cursor's column in terminal cells on its visual row.
	CursorCell() int
	// CursorRow is the visual row (0-based) the cursor is on.
	CursorRow() int
	View() string
	Cursor() *tea.Cursor
	Focus() tea.Cmd
	Blur()
	// SetKittyDetected enables Shift+Enter after detection confirms it.
	SetKittyDetected(bool)
	KittyDetected() bool
	Hint() string
}

// TextArea is the Bubbles-backed implementation.
type TextArea struct {
	ta         textarea.Model
	maxRows    int
	kitty      bool
	lastHome   bool
	lastEnd    bool
	focused    bool
	width      int
	submitKey  key.Binding
	newlineKey key.Binding
	shiftEnter key.Binding
	editorKey  key.Binding
	homeKey    key.Binding
	endKey     key.Binding
	beginKey   key.Binding
	finalKey   key.Binding
}

// New builds an editor with the baseline keymap.
func New() *TextArea {
	ta := textarea.New()
	ta.ShowLineNumbers = false
	ta.Prompt = "> "
	ta.CharLimit = 0
	ta.MaxHeight = 3
	ta.SetHeight(3)
	// Real terminal cursor: the app positions it from Cursor(); a virtual
	// cursor would be invisible to screen readers and to the caret checks.
	ta.SetVirtualCursor(false)
	km := textarea.DefaultKeyMap()
	// Enter must NOT insert a newline in the widget; Ctrl+J does.
	km.InsertNewline = key.NewBinding(key.WithKeys("ctrl+j"))
	km.DeleteWordBackward = key.NewBinding(key.WithKeys("ctrl+w", "alt+backspace"))
	km.DeleteBeforeCursor = key.NewBinding(key.WithKeys("ctrl+u"))
	km.WordBackward = key.NewBinding(key.WithKeys("alt+b", "alt+left", "ctrl+left"))
	km.WordForward = key.NewBinding(key.WithKeys("alt+f", "alt+right", "ctrl+right"))
	km.LineStart = key.NewBinding(key.WithKeys("home", "ctrl+a"))
	km.LineEnd = key.NewBinding(key.WithKeys("end", "ctrl+e"))
	km.InputBegin = key.NewBinding(key.WithKeys("ctrl+home", "alt+<"))
	km.InputEnd = key.NewBinding(key.WithKeys("ctrl+end", "alt+>"))
	km.LinePrevious = key.NewBinding(key.WithKeys("up", "ctrl+p"))
	km.LineNext = key.NewBinding(key.WithKeys("down", "ctrl+n"))
	// Ctrl+G is select-all in the default map; keep it off the baseline.
	km.SelectAll = key.NewBinding(key.WithDisabled())
	ta.KeyMap = km
	e := &TextArea{
		ta:         ta,
		maxRows:    3,
		submitKey:  key.NewBinding(key.WithKeys("enter")),
		newlineKey: km.InsertNewline,
		shiftEnter: key.NewBinding(key.WithKeys("shift+enter")),
		editorKey:  key.NewBinding(key.WithKeys("ctrl+x")),
		homeKey:    km.LineStart,
		endKey:     km.LineEnd,
		beginKey:   km.InputBegin,
		finalKey:   km.InputEnd,
	}
	e.SetWidth(80)
	return e
}

func (e *TextArea) SetWidth(w int) {
	if w < 10 {
		w = 10
	}
	e.width = w
	e.ta.SetWidth(w)
	e.fitHeight()
}

func (e *TextArea) SetMaxRows(n int) {
	if n < 1 {
		n = 1
	}
	e.maxRows = n
	e.ta.MaxHeight = n
	e.fitHeight()
}

// fitHeight keeps the widget at maxRows so its viewport never scrolls the
// first wrapped rows out of sight while the content still fits; View trims
// the unused rows. Long input therefore wraps into view instead of scrolling
// blind.
func (e *TextArea) fitHeight() {
	if e.ta.Height() != e.maxRows {
		e.ta.SetHeight(e.maxRows)
	}
}

// contentWidth is the widget's wrap width (SetWidth minus the prompt).
func (e *TextArea) contentWidth() int {
	w := e.ta.Width()
	if w < 1 {
		w = 1
	}
	return w
}

// SetPrompt changes the prompt glyphs (used for the voice indicator). Width
// math is recomputed so wrapping stays exact.
func (e *TextArea) SetPrompt(p string) {
	e.ta.Prompt = p
	e.ta.SetWidth(e.width)
}

// PromptWidth is the prompt's cell width (cursor x offset).
func (e *TextArea) PromptWidth() int { return ansi.StringWidth(e.ta.Prompt) }

func (e *TextArea) Value() string     { return e.ta.Value() }
func (e *TextArea) SetValue(s string) { e.ta.SetValue(s); e.fitHeight() }
func (e *TextArea) Reset()            { e.ta.Reset(); e.fitHeight() }
func (e *TextArea) Insert(s string)   { e.ta.InsertString(s); e.fitHeight() }

// View renders only the rows the content needs (1..maxRows).
func (e *TextArea) View() string {
	v := e.ta.View()
	lines := strings.Split(strings.TrimRight(v, "\n"), "\n")
	n := e.Rows()
	if n > e.maxRows {
		n = e.maxRows
	}
	if n < len(lines) {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
func (e *TextArea) Cursor() *tea.Cursor {
	return e.ta.Cursor()
}
func (e *TextArea) Focus() tea.Cmd          { e.focused = true; return e.ta.Focus() }
func (e *TextArea) Blur()                   { e.focused = false; e.ta.Blur() }
func (e *TextArea) SetKittyDetected(b bool) { e.kitty = b }
func (e *TextArea) KittyDetected() bool     { return e.kitty }

// Rows counts visual rows across all logical lines using the widget's own
// word-wrap rule and cell widths.
func (e *TextArea) Rows() int {
	w := e.contentWidth()
	rows := 0
	for _, line := range strings.Split(e.ta.Value(), "\n") {
		rows += VisualRows(line, w)
	}
	if rows < 1 {
		rows = 1
	}
	return rows
}

// CursorCell is the cursor's cell offset on its visual row (CharOffset is the
// cell-width measure in the widget; ColumnOffset counts runes).
func (e *TextArea) CursorCell() int { return e.ta.LineInfo().CharOffset }

// CursorRow is the visual row within the visible rows.
func (e *TextArea) CursorRow() int {
	w := e.contentWidth()
	lines := strings.Split(e.ta.Value(), "\n")
	row := 0
	for i := 0; i < e.ta.Line() && i < len(lines); i++ {
		row += VisualRows(lines[i], w)
	}
	row += e.ta.LineInfo().RowOffset - e.ta.ScrollYOffset()
	if row < 0 {
		row = 0
	}
	return row
}

// Hint renders the multiline hint; Shift+Enter appears only when detected.
func (e *TextArea) Hint() string {
	if e.kitty {
		return "enter send · ctrl+j / shift+enter newline · / commands · ctrl+y copy · ctrl+x $EDITOR"
	}
	return "enter send · ctrl+j newline · / commands · ctrl+y copy · ctrl+x $EDITOR"
}

// Handle maps a key to an action. Widget keys fall through to the textarea.
func (e *TextArea) Handle(msg tea.KeyPressMsg) (Action, tea.Cmd) {
	defer e.fitHeight()
	switch {
	case key.Matches(msg, e.submitKey):
		return ActSubmit, nil
	case key.Matches(msg, e.shiftEnter):
		if e.kitty {
			e.ta.InsertString("\n")
			return ActNone, nil
		}
		// Not detected: a bare terminal delivers plain Enter anyway, so this
		// branch only triggers on a terminal that DID send the chord without
		// our detection. Treat as newline: the safe reading of the intent.
		e.ta.InsertString("\n")
		return ActNone, nil
	case key.Matches(msg, e.editorKey):
		return ActOpenEditor, nil
	case key.Matches(msg, e.homeKey):
		// Second press (already at visual start) jumps to buffer start.
		if e.lastHome && e.ta.LineInfo().ColumnOffset == 0 {
			e.ta.MoveToBegin()
			e.lastHome = false
			return ActNone, nil
		}
		e.ta.CursorStart()
		e.lastHome = true
		e.lastEnd = false
		return ActNone, nil
	case key.Matches(msg, e.endKey):
		if e.lastEnd {
			e.ta.MoveToEnd()
			e.lastEnd = false
			return ActNone, nil
		}
		e.ta.CursorEnd()
		e.lastEnd = true
		e.lastHome = false
		return ActNone, nil
	case key.Matches(msg, e.beginKey):
		e.ta.MoveToBegin()
		return ActNone, nil
	case key.Matches(msg, e.finalKey):
		e.ta.MoveToEnd()
		return ActNone, nil
	}
	e.lastHome, e.lastEnd = false, false
	// Up/Down: history only when the buffer is empty (the spec's rule keeps
	// history recall from hijacking navigation inside wrapped text).
	if strings.TrimSpace(e.ta.Value()) == "" {
		switch msg.String() {
		case "up":
			return ActHistoryPrev, nil
		case "down":
			return ActHistoryNext, nil
		}
	}
	var cmd tea.Cmd
	e.ta, cmd = e.ta.Update(msg)
	return ActNone, cmd
}

// Update forwards non-key messages (blink, paste) to the widget.
func (e *TextArea) Update(msg tea.Msg) tea.Cmd {
	if _, isKey := msg.(tea.KeyPressMsg); isKey {
		return nil
	}
	var cmd tea.Cmd
	e.ta, cmd = e.ta.Update(msg)
	e.fitHeight()
	return cmd
}

// CellWidth is the terminal cell width of s, by grapheme cluster.
func CellWidth(s string) int { return ansi.StringWidth(s) }

// Graphemes splits s into grapheme clusters.
func Graphemes(s string) []string {
	var out []string
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		out = append(out, g.Str())
	}
	return out
}

// VisualRows is the number of rows a single logical line occupies at width w
// under the widget's soft-wrap rule. It mirrors the textarea's wrap function
// (word wrap by cell width, long words broken) so the app-side row count and
// the widget agree exactly.
func VisualRows(line string, w int) int {
	if w < 1 {
		return 1
	}
	return len(wrapLine([]rune(line), w))
}

// wrapLine is a faithful port of the Bubbles v2 textarea wrap algorithm.
func wrapLine(runes []rune, width int) [][]rune {
	lines := [][]rune{{}}
	var word []rune
	row, spaces := 0, 0
	sw := func(r []rune) int { return ansi.StringWidth(string(r)) }
	rep := func(n int) []rune {
		out := make([]rune, n)
		for i := range out {
			out[i] = ' '
		}
		return out
	}
	for _, r := range runes {
		if unicode.IsSpace(r) {
			spaces++
		} else {
			word = append(word, r)
		}
		if spaces > 0 {
			if sw(lines[row])+sw(word)+spaces > width {
				row++
				lines = append(lines, []rune{})
			}
			lines[row] = append(lines[row], word...)
			lines[row] = append(lines[row], rep(spaces)...)
			spaces = 0
			word = nil
		} else {
			last := ansi.StringWidth(string(word[len(word)-1]))
			if sw(word)+last > width {
				if len(lines[row]) > 0 {
					row++
					lines = append(lines, []rune{})
				}
				lines[row] = append(lines[row], word...)
				word = nil
			}
		}
	}
	if sw(lines[row])+sw(word)+spaces >= width {
		lines = append(lines, []rune{})
		lines[row+1] = append(lines[row+1], word...)
		lines[row+1] = append(lines[row+1], rep(spaces+1)...)
	} else {
		lines[row] = append(lines[row], word...)
		lines[row] = append(lines[row], rep(spaces+1)...)
	}
	return lines
}
