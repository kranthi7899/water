package chat

import (
	"context"
	"fmt"
	"image/color"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"water/internal/agent"
	"water/internal/backend"
	"water/internal/editor"
	"water/internal/layout"
	"water/internal/roles"
	"water/internal/session"
	"water/internal/theme"
)

// BackendInfo is what the header and /status show for the current role.
type BackendInfo struct {
	Name   string
	Reason string
	Auth   string
}

// Options wire the TUI to the app without importing it.
type Options struct {
	Registry      *roles.Registry
	Themes        fs.FS
	ForceTheme    string // ui.theme: "" = per-role
	EnvFor        func(role *roles.Role) (agent.Env, BackendInfo, error)
	StoreFor      func(role *roles.Role) *session.Store
	SwitchBackend func(role *roles.Role, name string) (backend.Backend, string, error)
	Retention     session.Retention
	StartRole     string // "" with Picker=true opens the picker
	ResumeSlug    string
	Picker        bool
	Voice         func(text string) error
	Profile       theme.Profile
	BudgetLine    func() string
	OnRateLimit   func(rl *backend.RateLimit)
}

type mode int

const (
	modePicker mode = iota
	modeChat
	modeConfirm
)

type turnMsg struct {
	turn Turn
	err  error
}

type cmdMsg struct {
	res Result
}

type editorDoneMsg struct {
	text string
	err  error
}

type tickMsg time.Time

// ExitSummary is printed after the alternate screen closes, like a shell
// tells you how to get back: the resume command and what the session held.
type ExitSummary struct {
	Role     string
	Slug     string
	Turns    int
	Words    int
	Duration time.Duration
}

func (e ExitSummary) String() string {
	if e.Role == "" {
		return "exited water"
	}
	var sb strings.Builder
	sb.WriteString("exited water · " + e.Role)
	if e.Slug != "" {
		fmt.Fprintf(&sb, "\nresume this session with:\n  water chat %s --resume %s\n", e.Role, e.Slug)
		fmt.Fprintf(&sb, "session   %s\nturns     %d (%d reply words)\n", e.Slug, e.Turns, e.Words)
		if e.Duration > 0 {
			fmt.Fprintf(&sb, "duration  %s\n", e.Duration.Round(time.Second))
		}
	} else {
		sb.WriteString(" (no messages were sent)\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

type model struct {
	opts    Options
	ctx     context.Context
	mode    mode
	width   int
	height  int
	regions layout.Regions

	// picker
	pick   int
	slugs  []string
	themes map[string]*theme.Theme

	// chat
	role     *roles.Role
	theme    *theme.Theme
	styler   theme.Styler
	sess     *Session
	info     BackendInfo
	ed       *editor.TextArea
	vp       viewport.Model
	showHero bool
	busy     bool
	busyText string
	busyAt   time.Time
	status   string
	summary  ExitSummary
	started  time.Time
	kitty    bool
	pending  string // slug awaiting delete confirmation
	lines    []string
	err      error
	quitting bool
}

// Run starts the interactive program.
func Run(ctx context.Context, o Options) error {
	if o.Profile == 0 && os.Getenv("NO_COLOR") == "" {
		o.Profile = theme.EnvProfile()
	}
	m := &model{opts: o, ctx: ctx, themes: map[string]*theme.Theme{}}
	for _, r := range o.Registry.All() {
		m.slugs = append(m.slugs, r.Slug)
		m.themes[r.Slug] = m.loadTheme(r.Slug)
	}
	m.mode = modePicker
	if o.StartRole != "" && !o.Picker {
		if err := m.enterRole(o.StartRole, o.ResumeSlug); err != nil {
			return err
		}
	}
	m.started = time.Now()
	p := tea.NewProgram(m, tea.WithContext(ctx))
	final, err := p.Run()
	if err != nil {
		return err
	}
	if fm, ok := final.(*model); ok {
		if fm.err != nil {
			return fm.err
		}
		fmt.Fprintln(os.Stderr, fm.exitSummary().String())
	}
	return nil
}

func (m *model) exitSummary() ExitSummary {
	if m.sess == nil {
		return ExitSummary{}
	}
	turns, words, _ := m.sess.Stats()
	return ExitSummary{Role: m.role.Slug, Slug: m.sess.Slug, Turns: turns, Words: words, Duration: time.Since(m.started)}
}

func (m *model) loadTheme(slug string) *theme.Theme {
	name := slug
	if m.opts.ForceTheme != "" {
		name = m.opts.ForceTheme
	}
	if t, err := theme.Load(m.opts.Themes, name); err == nil {
		return t
	}
	t, _ := theme.ForRole(m.opts.Themes, slug)
	return t
}

func (m *model) enterRole(slug, resume string) error {
	r, ok := m.opts.Registry.Get(slug)
	if !ok {
		return fmt.Errorf("unknown role %q", slug)
	}
	env, info, err := m.opts.EnvFor(r)
	if err != nil {
		return err
	}
	store := m.opts.StoreFor(r)
	s, err := Open(m.opts.Registry, r, env, store, resume)
	if err != nil {
		return err
	}
	s.BackendName, s.BackendReason = info.Name, info.Reason
	s.Retention = m.opts.Retention
	s.Summariser = ModelSummariser(r, env)
	s.Voice = m.opts.Voice
	s.BudgetLine = m.opts.BudgetLine
	s.OnRateLimit = m.opts.OnRateLimit
	if m.opts.SwitchBackend != nil {
		s.SwitchBackend = func(name string) (backend.Backend, string, error) { return m.opts.SwitchBackend(r, name) }
	}
	m.role, m.sess, m.info = r, s, info
	m.theme = m.themes[r.Slug]
	if m.theme == nil {
		m.theme = m.loadTheme(r.Slug)
	}
	m.styler = theme.Styler{Profile: m.opts.Profile}
	m.ed = editor.New()
	m.ed.SetKittyDetected(m.kitty)
	m.ed.Focus()
	m.applyPrompt()
	m.vp = viewport.New()
	m.showHero = len(s.Turns()) == 0
	m.mode = modeChat
	m.status = ""
	m.rebuildTranscript()
	m.relayout()
	return nil
}

func (m *model) Init() tea.Cmd { return nil }

// applyPrompt renders the voice indicator to the LEFT of the composer: a
// filled note when replies are spoken, a hollow one when voice is available
// but off, nothing when no provider exists.
func (m *model) applyPrompt() {
	if m.ed == nil || m.sess == nil {
		return
	}
	switch {
	case m.sess.Voice == nil:
		m.ed.SetPrompt("> ")
	case m.sess.VoiceOn:
		m.ed.SetPrompt("♪ > ")
	default:
		m.ed.SetPrompt("○ > ")
	}
}

func tick() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) relayout() {
	if m.width == 0 || m.height == 0 {
		return
	}
	rows := 1
	if m.ed != nil {
		rows = m.ed.Rows()
	}
	r, err := layout.Compute(layout.Spec{Width: m.width, Height: m.height, ShowHero: m.showHero, InputRows: rows})
	if err != nil {
		// Too small: fall back to a one-row header, no hero.
		r, _ = layout.Compute(layout.Spec{Width: m.width, Height: max(m.height, 8), ShowHero: false, InputRows: 1})
	}
	m.regions = r
	if m.ed != nil {
		m.ed.SetWidth(r.Input.W)
		m.ed.SetMaxRows(3)
	}
	m.vp.SetWidth(r.Chat.W)
	m.vp.SetHeight(r.Chat.H)
	m.vp.SetContent(strings.Join(m.lines, "\n"))
	m.vp.GotoBottom()
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.relayout()
		return m, nil
	case tea.KeyboardEnhancementsMsg:
		m.kitty = msg.SupportsKeyDisambiguation()
		if m.ed != nil {
			m.ed.SetKittyDetected(m.kitty)
		}
		return m, nil
	case tea.MouseWheelMsg:
		if m.mode == modeChat {
			var cmd tea.Cmd
			m.vp, cmd = m.vp.Update(msg)
			return m, cmd
		}
		return m, nil
	case tickMsg:
		if m.busy {
			m.status = fmt.Sprintf("%s (%s)", m.busyText, time.Since(m.busyAt).Round(100*time.Millisecond))
			return m, tick()
		}
		return m, nil
	case turnMsg:
		m.busy = false
		if msg.err != nil {
			m.status = "error: " + msg.err.Error()
		} else {
			m.status = fmt.Sprintf("%s · %s", msg.turn.Backend, msg.turn.Duration.Round(time.Millisecond))
		}
		m.rebuildTranscript()
		m.relayout()
		return m, nil
	case cmdMsg:
		m.busy = false
		return m, m.applyResult(msg.res)
	case editorDoneMsg:
		m.busy = false
		if msg.err != nil {
			m.status = "editor: " + msg.err.Error()
			return m, nil
		}
		if strings.TrimSpace(msg.text) != "" {
			m.ed.SetValue(msg.text)
		}
		m.relayout()
		return m, nil
	case tea.KeyPressMsg:
		switch m.mode {
		case modePicker:
			return m.updatePicker(msg)
		case modeConfirm:
			return m.updateConfirm(msg)
		default:
			return m.updateChat(msg)
		}
	}
	if m.mode == modeChat && m.ed != nil {
		return m, m.ed.Update(msg)
	}
	return m, nil
}

func (m *model) updatePicker(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c", "q", "esc":
		m.quitting = true
		return m, tea.Quit
	case "left", "h", "shift+tab":
		if m.pick > 0 {
			m.pick--
		}
	case "right", "l", "tab":
		if m.pick < len(m.slugs)-1 {
			m.pick++
		}
	case "enter":
		if err := m.enterRole(m.slugs[m.pick], ""); err != nil {
			m.err = err
			return m, tea.Quit
		}
	default:
		if r := msg.Code; r >= '1' && r <= '9' && int(r-'1') < len(m.slugs) {
			m.pick = int(r - '1')
			if err := m.enterRole(m.slugs[m.pick], ""); err != nil {
				m.err = err
				return m, tea.Quit
			}
		}
	}
	return m, nil
}

func (m *model) updateConfirm(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch strings.ToLower(msg.String()) {
	case "y":
		slug := m.pending
		m.pending = ""
		m.mode = modeChat
		if err := m.sess.ConfirmDelete(slug); err != nil {
			m.status = "delete: " + err.Error()
		} else {
			m.status = "deleted " + slug
			m.rebuildTranscript()
			m.relayout()
		}
	case "n", "esc", "ctrl+c":
		m.pending = ""
		m.mode = modeChat
		m.status = "kept"
	}
	return m, nil
}

func (m *model) updateChat(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if strings.TrimSpace(m.ed.Value()) != "" {
			m.ed.Reset()
			m.relayout()
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit
	case "ctrl+d":
		if strings.TrimSpace(m.ed.Value()) == "" {
			m.quitting = true
			return m, tea.Quit
		}
	case "pgup":
		m.vp.PageUp()
		return m, nil
	case "pgdown":
		m.vp.PageDown()
		return m, nil
	case "ctrl+y":
		if text, ok := m.sess.LastReply(1); ok {
			if how, err := CopyToClipboard(text); err != nil {
				m.status = "copy: " + err.Error()
			} else {
				m.status = "copied last reply via " + how
			}
		} else {
			m.status = "nothing to copy yet"
		}
		return m, nil
	case "ctrl+b":
		res := Dispatch(m.ctx, m.sess, map[bool]string{true: "/voice off", false: "/voice on"}[m.sess.VoiceOn])
		return m, m.applyResult(res)
	case "tab":
		if matches := Complete(m.ed.Value()); len(matches) > 0 {
			m.ed.SetValue("/" + matches[0].Name + " ")
			m.relayout()
		}
		return m, nil
	}
	if m.busy {
		m.status = m.busyText + " (input paused)"
		return m, nil
	}
	act, cmd := m.ed.Handle(msg)
	switch act {
	case editor.ActSubmit:
		text := strings.TrimSpace(m.ed.Value())
		if text == "" {
			return m, nil
		}
		m.ed.Reset()
		m.relayout()
		if IsCommand(text) {
			return m, m.runCommand(text)
		}
		return m, m.runTurn(text)
	case editor.ActOpenEditor:
		return m, m.openEditor()
	case editor.ActHistoryPrev:
		if h, ok := m.sess.HistoryPrev(); ok {
			m.ed.SetValue(h)
		}
	case editor.ActHistoryNext:
		if h, ok := m.sess.HistoryNext(); ok {
			m.ed.SetValue(h)
		} else {
			m.ed.Reset()
		}
	}
	m.relayout()
	return m, cmd
}

func (m *model) runTurn(text string) tea.Cmd {
	m.busy, m.busyText, m.busyAt = true, "thinking as "+m.role.Slug+"…", time.Now()
	m.status = m.busyText
	m.showHero = false
	m.lines = append(m.lines, m.renderUser(text)...)
	m.relayout()
	s := m.sess
	ctx := m.ctx
	return tea.Batch(tick(), func() tea.Msg {
		t, err := s.Send(ctx, text)
		return turnMsg{turn: t, err: err}
	})
}

func (m *model) runCommand(text string) tea.Cmd {
	m.busy, m.busyText, m.busyAt = true, "running "+strings.Fields(text)[0], time.Now()
	m.status = m.busyText
	s := m.sess
	ctx := m.ctx
	return tea.Batch(tick(), func() tea.Msg { return cmdMsg{res: Dispatch(ctx, s, text)} })
}

func (m *model) applyResult(res Result) tea.Cmd {
	if res.Err != nil {
		m.status = "error: " + res.Err.Error()
		m.lines = append(m.lines, m.renderSystem("error: "+res.Err.Error())...)
		m.relayout()
		return nil
	}
	switch res.Action {
	case ActQuit:
		m.quitting = true
		return tea.Quit
	case ActSwitchRole:
		if err := m.enterRole(res.Arg, ""); err != nil {
			m.status = err.Error()
		}
		return nil
	case ActOpenPicker:
		m.mode = modePicker
		return nil
	case ActConfirmDelete:
		m.pending = res.Arg
		m.mode = modeConfirm
		m.status = res.Output + " [y/N]"
		return nil
	case ActOpenEditor:
		return m.openEditor()
	case ActRedraw:
		m.applyPrompt()
		m.rebuildTranscript()
		if res.Output != "" {
			m.lines = append(m.lines, m.renderSystem(res.Output)...)
		}
		m.status = "ok"
		m.relayout()
		return nil
	case ActResend:
		return m.runTurn(res.Arg)
	}
	if res.Output != "" {
		m.lines = append(m.lines, m.renderSystem(res.Output)...)
	}
	m.status = "ok"
	m.rebuildTranscriptIfContextChanged()
	m.relayout()
	return nil
}

func (m *model) rebuildTranscriptIfContextChanged() {
	// After /clear, /resume, /compact the turns changed: rebuild but keep
	// the system output appended above.
	sys := m.lines
	m.rebuildTranscript()
	if len(m.sess.Turns()) == 0 && m.sess.Summary() == "" {
		// keep the command output visible on an otherwise empty context
		m.lines = append(m.lines, sys[max(0, len(sys)-12):]...)
	}
}

func (m *model) openEditor() tea.Cmd {
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		m.status = "$EDITOR is not set"
		return nil
	}
	f, err := os.CreateTemp("", "water-msg-*.md")
	if err != nil {
		m.status = err.Error()
		return nil
	}
	_ = os.Chmod(f.Name(), 0o600)
	_, _ = f.WriteString(m.ed.Value())
	f.Close()
	parts := strings.Fields(ed)
	c := exec.Command(parts[0], append(parts[1:], f.Name())...)
	m.busy, m.busyText = true, "in $EDITOR"
	return tea.ExecProcess(c, func(err error) tea.Msg {
		defer os.Remove(f.Name())
		if err != nil {
			return editorDoneMsg{err: err}
		}
		b, rerr := os.ReadFile(f.Name())
		return editorDoneMsg{text: strings.TrimSpace(string(b)), err: rerr}
	})
}

// --- rendering --------------------------------------------------------------

func (m *model) fg(hex, s string) string { return m.styler.Fg(hex, s) }

func (m *model) wrap(s string, w int) []string {
	if w < 8 {
		w = 8
	}
	var out []string
	for _, para := range strings.Split(s, "\n") {
		if strings.TrimSpace(para) == "" {
			out = append(out, "")
			continue
		}
		wrapped := ansi.Hardwrap(ansi.Wordwrap(para, w, ""), w, true)
		out = append(out, strings.Split(wrapped, "\n")...)
	}
	return out
}

func (m *model) renderUser(text string) []string {
	w := m.regions.Chat.W
	if w == 0 {
		w = 80
	}
	lines := []string{m.fg(m.theme.Palette.Muted, m.theme.Label("you"))}
	for _, l := range m.wrap(text, w-2) {
		lines = append(lines, "  "+l)
	}
	return append(lines, "")
}

func (m *model) renderReply(t Turn) []string {
	w := m.regions.Chat.W
	if w == 0 {
		w = 80
	}
	label := m.theme.Label(m.role.Slug)
	if t.Flagged {
		label += " " + m.theme.Label("flagged")
	}
	if t.Untrusted {
		label += " " + m.theme.Label("untrusted input")
	}
	lines := []string{m.fg(m.theme.Palette.Accent, label)}
	for _, l := range m.wrap(t.Reply, w-2) {
		lines = append(lines, "  "+m.fg(m.theme.Palette.Foreground, l))
	}
	return append(lines, "")
}

func (m *model) renderSystem(text string) []string {
	w := m.regions.Chat.W
	if w == 0 {
		w = 80
	}
	var lines []string
	for _, l := range m.wrap(text, w-2) {
		lines = append(lines, m.fg(m.theme.Palette.Muted, "  "+l))
	}
	return append(lines, "")
}

func (m *model) rebuildTranscript() {
	m.lines = nil
	if m.sess == nil {
		return
	}
	if s := m.sess.Summary(); s != "" {
		m.lines = append(m.lines, m.fg(m.theme.Palette.Muted, m.theme.Label("compacted context")))
		m.lines = append(m.lines, m.renderSystem(s)...)
	}
	for _, t := range m.sess.Turns() {
		m.lines = append(m.lines, m.renderUser(t.User)...)
		m.lines = append(m.lines, m.renderReply(t)...)
	}
}

func (m *model) header() []string {
	p := m.theme.Palette
	rule := m.fg(p.Accent, strings.Repeat(m.theme.Rule(), m.width))
	left := m.theme.Label(fmt.Sprintf(" water · %s ", m.role.Name))
	if m.theme.Role == "" {
		left = m.theme.Label(fmt.Sprintf(" %s · %s ", m.theme.Name, m.role.Name))
	}
	right := m.theme.Label(fmt.Sprintf(" %s · %s ", m.info.Name, m.info.Auth))
	gap := m.width - ansi.StringWidth(left) - ansi.StringWidth(right)
	if gap < 1 {
		gap = 1
	}
	band := m.styler.Bg(p.Panel, m.fg(p.Foreground, left+strings.Repeat(" ", gap)+right))
	rows := []string{rule, band, m.fg(p.Muted, strings.Repeat(m.theme.Rule(), m.width))}
	if m.regions.Header.H == 1 {
		return rows[1:2]
	}
	return rows
}

func (m *model) hero() []string {
	r := m.regions.Hero
	if !r.Visible() {
		return nil
	}
	art := layout.ScaleArt(m.theme.Hero.Lines, r.W, r.H-1)
	rows := make([]string, 0, r.H)
	title := m.theme.Label(m.theme.Hero.Title)
	pad := (r.W - ansi.StringWidth(title)) / 2
	if pad < 0 {
		pad = 0
	}
	rows = append(rows, m.fg(m.theme.Palette.Foreground, strings.Repeat(" ", pad)+title))
	for _, l := range art {
		rows = append(rows, m.fg(m.theme.Palette.Accent, l))
	}
	for len(rows) < r.H {
		rows = append(rows, "")
	}
	return rows[:r.H]
}

// contextBar renders the fill of the model's context window from the last
// turn: green under 50%, yellow to 80%, red above (thresholds as in Hermes).
func (m *model) contextBar() string {
	turns := m.sess.Turns()
	if len(turns) == 0 {
		return ""
	}
	t := turns[len(turns)-1]
	if t.ContextWindow <= 0 || t.InputTokens <= 0 {
		return ""
	}
	frac := float64(t.InputTokens) / float64(t.ContextWindow)
	if frac > 1 {
		frac = 1
	}
	cells := 8
	filled := int(frac*float64(cells) + 0.5)
	bar := strings.Repeat("▰", filled) + strings.Repeat("▱", cells-filled)
	col := "#2BE82B"
	switch {
	case frac >= 0.8:
		col = "#D00000"
	case frac >= 0.5:
		col = "#E8B400"
	}
	return m.fg(col, fmt.Sprintf("ctx %s %d%%", bar, int(frac*100+0.5))) + m.fg(m.theme.Palette.Muted, fmt.Sprintf(" %s/%s", kilo(t.InputTokens), kilo(t.ContextWindow)))
}

func (m *model) statusLine() string {
	p := m.theme.Palette
	slug := "new session"
	if m.sess != nil && m.sess.Slug != "" {
		slug = m.sess.Slug
		if m.sess.Pinned {
			slug += " *"
		}
	}
	left := m.theme.Label(fmt.Sprintf(" %s · %s ", m.info.Name, slug))
	mid := m.status
	if mid == "" {
		mid = m.ed.Hint()
	}
	if cb := m.contextBar(); cb != "" {
		mid = cb + "  " + mid
	}
	line := left + " " + mid
	if n := ansi.StringWidth(line); n < m.width {
		line += strings.Repeat(" ", m.width-n)
	}
	return m.styler.Bg(p.Panel, m.fg(p.Foreground, ansi.Truncate(line, m.width, "")))
}

func fit(lines []string, h, w int) []string {
	out := make([]string, 0, h)
	for _, l := range lines {
		if len(out) >= h {
			break
		}
		out = append(out, ansi.Truncate(l, w, ""))
	}
	for len(out) < h {
		out = append(out, "")
	}
	return out
}

func (m *model) View() tea.View {
	if m.width == 0 {
		return tea.NewView("")
	}
	var v tea.View
	v.AltScreen = true
	// Mouse capture is deliberately OFF so the terminal's own text selection
	// and copy keep working over the transcript (PgUp/PgDn scroll).
	v.MouseMode = tea.MouseModeNone
	v.KeyboardEnhancements = tea.KeyboardEnhancements{ReportAlternateKeys: true}
	if m.mode == modePicker {
		v.Content = m.pickerView()
		bt, _ := theme.Load(m.opts.Themes, "water-base")
		if bt != nil {
			if c, err := theme.ParseHex(bt.Palette.Background); err == nil && m.opts.Profile != theme.None {
				v.BackgroundColor = c.Color()
			}
		}
		return v
	}
	r := m.regions
	var rows []string
	rows = append(rows, fit(m.header(), r.Header.H, m.width)...)
	if r.Hero.Visible() {
		rows = append(rows, fit(m.hero(), r.Hero.H, m.width)...)
	}
	chatLines := strings.Split(m.vp.View(), "\n")
	// Slash autocomplete: a dropdown drawn over the bottom of the CHAT
	// region (content, not geometry: CHAT/INPUT rows are unchanged).
	if matches := Complete(m.ed.Value()); len(matches) > 0 && !m.busy {
		limit := 6
		if limit > r.Chat.H-1 {
			limit = r.Chat.H - 1
		}
		var drop []string
		for i, c := range matches {
			if i >= limit {
				break
			}
			mark := "  "
			if i == 0 {
				mark = "▶ "
			}
			drop = append(drop, m.styler.Bg(m.theme.Palette.Panel, m.fg(m.theme.Palette.Foreground, fmt.Sprintf(" %s%-34s %s", mark, c.Usage, c.Help))))
		}
		drop = append(drop, m.fg(m.theme.Palette.Muted, "  tab completes · enter runs"))
		chatLines = fit(chatLines, r.Chat.H, m.width)
		start := r.Chat.H - len(drop)
		if start < 0 {
			start = 0
		}
		chatLines = append(chatLines[:start], drop...)
	}
	rows = append(rows, fit(chatLines, r.Chat.H, m.width)...)
	inputLines := strings.Split(m.ed.View(), "\n")
	rows = append(rows, fit(inputLines, r.Input.H, m.width)...)
	rows = append(rows, m.statusLine())
	v.Content = strings.Join(rows, "\n")
	if c, err := theme.ParseHex(m.theme.Palette.Background); err == nil && m.opts.Profile != theme.None {
		v.BackgroundColor = c.Color()
	}
	if fc, err := theme.ParseHex(m.theme.Palette.Foreground); err == nil && m.opts.Profile != theme.None {
		v.ForegroundColor = fc.Color()
	}
	if m.mode == modeChat && !m.busy {
		cur := m.ed.Cursor()
		x, y := m.ed.PromptWidth()+m.ed.CursorCell(), m.ed.CursorRow()
		if cur != nil {
			x, y = cur.X, cur.Y
		}
		if y >= r.Input.H {
			y = r.Input.H - 1
		}
		var col color.Color
		if c, err := theme.ParseHex(m.theme.Palette.Accent); err == nil {
			col = c.Color()
		}
		v.Cursor = &tea.Cursor{Position: tea.Position{X: r.Input.Col + x, Y: r.Input.Row + y}, Color: col, Shape: tea.CursorBar, Blink: true}
	}
	return v
}

func (m *model) pickerView() string {
	n := len(m.slugs)
	if n == 0 {
		return "no roles"
	}
	colW := m.width / n
	if colW < 12 {
		colW = 12
	}
	artH := m.height - 10
	if artH > 12 {
		artH = 12
	}
	if artH < 0 {
		artH = 0
	}
	cols := make([][]string, n)
	for i, slug := range m.slugs {
		t := m.themes[slug]
		if t == nil {
			t, _ = theme.ForRole(m.opts.Themes, slug)
		}
		st := theme.Styler{Profile: m.opts.Profile}
		r, _ := m.opts.Registry.Get(slug)
		title := fmt.Sprintf("%d  %s", i+1, t.Label(r.Name))
		if i == m.pick {
			title = "▶ " + title
		} else {
			title = "  " + title
		}
		var lines []string
		lines = append(lines, st.Fg(t.Palette.Accent, strings.Repeat("-", colW-2)))
		lines = append(lines, st.Bold(st.Fg(t.Palette.Foreground, ansi.Truncate(title, colW-2, ""))))
		lines = append(lines, "")
		for _, a := range layout.ScaleArt(t.Hero.Lines, colW-4, artH) {
			lines = append(lines, st.Fg(t.Palette.Accent, " "+a))
		}
		lines = append(lines, "")
		for _, d := range m.wrap(r.Description, colW-4) {
			lines = append(lines, st.Fg(t.Palette.Muted, " "+d))
		}
		cols[i] = lines
	}
	maxH := 0
	for _, c := range cols {
		if len(c) > maxH {
			maxH = len(c)
		}
	}
	var sb strings.Builder
	base, _ := theme.Load(m.opts.Themes, "water-base")
	st := theme.Styler{Profile: m.opts.Profile}
	head := "  WATER · CHOOSE A ROLE   ←/→ or 1-" + fmt.Sprint(n) + " · enter to talk · q to quit"
	if base != nil {
		head = st.Fg(base.Palette.Foreground, head)
	}
	sb.WriteString(head + "\n\n")
	for row := 0; row < maxH && row < m.height-3; row++ {
		for i := range cols {
			cell := ""
			if row < len(cols[i]) {
				cell = cols[i][row]
			}
			cell = ansi.Truncate(cell, colW, "")
			pad := colW - ansi.StringWidth(cell)
			if pad < 0 {
				pad = 0
			}
			sb.WriteString(cell + strings.Repeat(" ", pad))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}
