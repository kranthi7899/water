package chat

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"water/internal/session"
)

// Action is what the UI must do after a command.
type Action int

const (
	ActNone Action = iota
	ActQuit
	ActSwitchRole
	ActOpenPicker
	ActConfirmDelete
	ActOpenEditor
	ActRedraw // context changed; rebuild the transcript view
	ActResend // Arg is the message to send again
)

func kilo(n int) string {
	switch {
	case n >= 1000000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	}
	return strconv.Itoa(n)
}

// Result of a dispatched command.
type Result struct {
	Output string
	Action Action
	Arg    string // role slug for ActSwitchRole, session slug for ActConfirmDelete
	Err    error
}

// Command describes a slash command for /help.
type Command struct {
	Name, Usage, Help string
}

// Commands is the registry (names converged with comparable tools).
var Commands = []Command{
	{"help", "/help", "list commands"},
	{"clear", "/clear", "reset the active context (transcript file is kept)"},
	{"compact", "/compact [focus]", "append a summary checkpoint for the active context"},
	{"resume", "/resume [slug]", "list sessions, or continue one by slug"},
	{"delete", "/delete [slug]", "delete a session (asks for confirmation)"},
	{"name", "/name <slug>", "pin this session under a name (exempt from pruning)"},
	{"model", "/model [name]", "show or set the model for this role in this session"},
	{"backend", "/backend [name]", "show or set this role's backend for this session"},
	{"status", "/status", "role, backend and why, session, attachments"},
	{"consult", "/consult <role> <question>", "ask another role; its answer arrives as a message, never a memory read"},
	{"switch", "/switch <role>", "talk to a different role (new inbox, isolated memory, its theme)"},
	{"agents", "/agents", "open the agent picker"},
	{"remember", "/remember [note]", "promote a note (or the last reply) into this role's curated memory"},
	{"why", "/why", "traceability for the last response: experience entries, skills, inbox"},
	{"skills", "/skills", "which skills loaded on the last turn and why"},
	{"flag", "/flag [reason]", "mark the last response bad for the feedback loop"},
	{"attach", "/attach <path> | /attach list | /attach clear", "attach a document, image, or text file for this session (@path works per turn)"},
	{"editor", "/editor", "compose the next message in $EDITOR"},
	{"copy", "/copy [N]", "copy the last (or Nth-from-last) reply to the clipboard (Ctrl+Y)"},
	{"undo", "/undo", "drop the last exchange from the active context (transcript is kept)"},
	{"retry", "/retry", "send the last message again"},
	{"usage", "/usage", "subscription budget and context usage"},
	{"save", "/save [file]", "export the active context as markdown"},
	{"title", "/title <name>", "alias of /name"},
	{"voice", "/voice [on|off|status]", "speak replies aloud (Ctrl+B toggles)"},
	{"quit", "/quit", "leave the session"},
}

// Complete returns the commands whose name starts with the typed prefix
// (without the slash), for the autocomplete dropdown. Empty prefix = all.
func Complete(line string) []Command {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "/") || strings.Contains(line, " ") {
		return nil
	}
	prefix := strings.ToLower(strings.TrimPrefix(line, "/"))
	var out []Command
	for _, c := range Commands {
		if strings.HasPrefix(c.Name, prefix) {
			out = append(out, c)
		}
	}
	return out
}

// CopyToClipboard uses the platform clipboard tool, falling back to the
// OSC 52 escape (which most terminals honour) when none exists.
func CopyToClipboard(text string) (how string, err error) {
	candidates := [][]string{{"pbcopy"}, {"wl-copy"}, {"xclip", "-selection", "clipboard"}, {"xsel", "--clipboard", "--input"}}
	for _, c := range candidates {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Stdin = strings.NewReader(text)
		if err := cmd.Run(); err == nil {
			return c[0], nil
		}
	}
	// OSC 52: write to the controlling terminal directly.
	tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		return "", errors.New("no clipboard tool found (pbcopy, wl-copy, xclip, xsel) and no terminal for OSC 52")
	}
	defer tty.Close()
	_, err = fmt.Fprintf(tty, "\x1b]52;c;%s\x07", base64.StdEncoding.EncodeToString([]byte(text)))
	return "osc52", err
}

// commandAliases are accepted names that are not listed separately in /help.
var commandAliases = map[string]bool{"?": true, "exit": true, "q": true}

// IsCommand reports whether a line is a slash command: its first word, after
// the slash, must be a known command name or alias. A line that merely starts
// with "/" — a dropped file path, "/etc/hosts has…", "/r/golang…" — is not a
// command and falls through to normal handling.
func IsCommand(line string) bool {
	t := strings.TrimSpace(line)
	if !strings.HasPrefix(t, "/") {
		return false
	}
	name := strings.ToLower(strings.TrimPrefix(strings.Fields(t)[0], "/"))
	if commandAliases[name] {
		return true
	}
	for _, c := range Commands {
		if c.Name == name {
			return true
		}
	}
	return false
}

// DroppedPath returns a cleaned file path when the whole input is a single
// existing file, as a terminal pastes it on drag-and-drop: possibly quoted,
// possibly with backslash-escaped spaces. ok is false for anything else.
func DroppedPath(line string) (string, bool) {
	p := NormalizePath(strings.TrimSpace(line))
	if p == "" || strings.ContainsAny(p, "\n\r") {
		return "", false
	}
	if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
		return p, true
	}
	return "", false
}

// LeadingAttachment returns a regular file explicitly quoted at the beginning
// of a message and the request that follows it. Terminal drag-and-drop often
// inserts a quoted path, then the person immediately types their request:
//
//	"/Users/me/Downloads/Board Deck.pdf" write a brief
//
// The quotes are intentional evidence that this is a file reference, rather
// than prose which happens to begin with a path. Only that unambiguous form is
// auto-attached; bare paths remain plain text unless they are the whole input
// (DroppedPath) or explicitly use @path.
func LeadingAttachment(line string) (path, request string, ok bool) {
	t := strings.TrimSpace(line)
	if len(t) < 3 || (t[0] != '\'' && t[0] != '"') {
		return "", "", false
	}
	quote := t[0]
	end := 1
	for end < len(t) {
		if t[end] == quote && (end == 0 || t[end-1] != '\\') {
			break
		}
		end++
	}
	if end == len(t) {
		return "", "", false
	}
	request = strings.TrimSpace(t[end+1:])
	if request == "" {
		return "", "", false
	}
	path = NormalizePath(t[:end+1])
	if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
		return path, request, true
	}
	return "", "", false
}

// NormalizePath undoes the quoting terminals apply to dragged paths and
// expands a leading ~.
func NormalizePath(p string) string {
	p = strings.TrimSpace(p)
	if len(p) >= 2 && (p[0] == '\'' && p[len(p)-1] == '\'' || p[0] == '"' && p[len(p)-1] == '"') {
		p = p[1 : len(p)-1]
	} else {
		p = strings.ReplaceAll(p, "\\ ", " ")
		p = strings.ReplaceAll(p, "\\(", "(")
		p = strings.ReplaceAll(p, "\\)", ")")
		p = strings.ReplaceAll(p, "\\'", "'")
	}
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(h, p[2:])
		}
	}
	return p
}

// Dispatch runs a slash command against the session.
func Dispatch(ctx context.Context, s *Session, line string) Result {
	line = strings.TrimSpace(line)
	name, rest, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
	name = strings.ToLower(strings.TrimSpace(name))
	rest = strings.TrimSpace(rest)
	switch name {
	case "help", "?":
		var sb strings.Builder
		for _, c := range Commands {
			sb.WriteString(fmt.Sprintf("  %-34s %s\n", c.Usage, c.Help))
		}
		return Result{Output: strings.TrimRight(sb.String(), "\n")}
	case "quit", "exit", "q":
		return Result{Action: ActQuit}
	case "clear":
		old := s.Slug
		s.Clear()
		if old != "" {
			return Result{Output: "context cleared; " + old + " is kept and resumable with /resume " + old}
		}
		return Result{Output: "context cleared"}
	case "compact":
		sum, err := s.Compact(ctx, rest)
		if err != nil {
			return Result{Err: err}
		}
		return Result{Output: "compacted:\n" + sum}
	case "resume":
		if rest == "" {
			infos, err := s.Store.List()
			if err != nil {
				return Result{Err: err}
			}
			if len(infos) == 0 {
				return Result{Output: "no sessions for " + s.Role.Slug}
			}
			var sb strings.Builder
			for _, in := range infos {
				pin := " "
				if in.Pinned {
					pin = "*"
				}
				name := in.Name
				if name != "" {
					name = " (" + name + ")"
				}
				sb.WriteString(fmt.Sprintf(" %s %-40s %3d turns  %s%s\n", pin, in.Slug, in.Turns, in.Modified.Format("2006-01-02 15:04"), name))
			}
			return Result{Output: strings.TrimRight(sb.String(), "\n")}
		}
		if err := s.resume(rest); err != nil {
			return Result{Err: err}
		}
		return Result{Output: fmt.Sprintf("resumed %s (%d turns in context)", rest, len(s.turns))}
	case "delete":
		slug := rest
		if slug == "" {
			slug = s.Slug
		}
		if slug == "" {
			return Result{Err: fmt.Errorf("no session to delete; /delete <slug>")}
		}
		if !s.Store.Exists(slug) {
			return Result{Err: fmt.Errorf("session %q not found", slug)}
		}
		return Result{Action: ActConfirmDelete, Arg: slug, Output: "delete session " + slug + "? this cannot be undone"}
	case "name":
		if rest == "" {
			return Result{Err: fmt.Errorf("usage: /name <slug>")}
		}
		if err := s.ensureSession(rest); err != nil {
			return Result{Err: err}
		}
		if err := s.Store.Pin(s.Slug, rest, true); err != nil {
			return Result{Err: err}
		}
		s.Name, s.Pinned = rest, true
		return Result{Output: "pinned " + s.Slug + " as " + rest + " (exempt from pruning)"}
	case "model":
		if rest == "" {
			m := s.Env.RoleModels[s.Role.Slug]
			if m == "" {
				m = "(backend default)"
			}
			return Result{Output: "model: " + m}
		}
		if s.Env.RoleModels == nil {
			s.Env.RoleModels = map[string]string{}
		}
		s.Env.RoleModels[s.Role.Slug] = rest
		return Result{Output: "model for " + s.Role.Slug + " set to " + rest + " for this session"}
	case "backend":
		if rest == "" {
			return Result{Output: fmt.Sprintf("backend: %s (%s)", s.BackendName, s.BackendReason)}
		}
		if s.SwitchBackend == nil {
			return Result{Err: fmt.Errorf("backend switching is not available here")}
		}
		b, reason, err := s.SwitchBackend(rest)
		if err != nil {
			return Result{Err: err}
		}
		if s.Env.RoleBackends == nil {
			s.Env.RoleBackends = map[string]backendIface{}
		}
		s.Env.RoleBackends[s.Role.Slug] = b
		s.BackendName, s.BackendReason = b.Name(), reason
		return Result{Output: "backend for " + s.Role.Slug + ": " + b.Name() + " (" + reason + ")"}
	case "status":
		var sb strings.Builder
		fmt.Fprintf(&sb, "role      %s (%s)\n", s.Role.Name, s.Role.Slug)
		fmt.Fprintf(&sb, "backend   %s — %s\n", s.BackendName, s.BackendReason)
		if m := s.Env.RoleModels[s.Role.Slug]; m != "" {
			fmt.Fprintf(&sb, "model     %s\n", m)
		}
		slug := s.Slug
		if slug == "" {
			slug = "(new; created on first message)"
		}
		fmt.Fprintf(&sb, "session   %s  turns %d  pinned %v\n", slug, len(s.turns), s.Pinned)
		if s.summary != "" {
			fmt.Fprintf(&sb, "context   compacted summary + %d turns\n", len(s.turns))
		}
		if len(s.attachments) > 0 {
			var names []string
			for _, a := range s.attachments {
				names = append(names, a.Name)
			}
			fmt.Fprintf(&sb, "attached  %s\n", strings.Join(names, ", "))
		}
		if pol := s.Env.RoleTools[s.Role.Slug]; pol != nil && !pol.Empty() {
			fmt.Fprintf(&sb, "tools     %s (roots: %s)\n", strings.Join(pol.ToolNames(), ", "), strings.Join(pol.Filesystem.Roots, ", "))
		} else {
			sb.WriteString("tools     none\n")
		}
		if s.BudgetLine != nil {
			fmt.Fprintf(&sb, "budget    %s\n", s.BudgetLine())
		}
		return Result{Output: strings.TrimRight(sb.String(), "\n")}
	case "consult":
		target, q, _ := strings.Cut(rest, " ")
		if target == "" || strings.TrimSpace(q) == "" {
			return Result{Err: fmt.Errorf("usage: /consult <role> <question>")}
		}
		ans, err := s.Consult(ctx, target, strings.TrimSpace(q))
		if err != nil {
			return Result{Err: err}
		}
		return Result{Output: fmt.Sprintf("%s answers (as a message to %s, now in your context):\n%s", ans.From, s.Role.Slug, ans.Payload)}
	case "switch":
		if rest == "" {
			return Result{Err: fmt.Errorf("usage: /switch <role> (%s)", strings.Join(s.Registry.Slugs(), ", "))}
		}
		if _, ok := s.Registry.Get(rest); !ok {
			return Result{Err: fmt.Errorf("unknown role %q (%s)", rest, strings.Join(s.Registry.Slugs(), ", "))}
		}
		return Result{Action: ActSwitchRole, Arg: strings.ToLower(rest)}
	case "agents":
		return Result{Action: ActOpenPicker}
	case "remember":
		e, err := s.Remember(ctx, rest)
		if err != nil {
			return Result{Err: err}
		}
		return Result{Output: "remembered as " + e.ID + " in " + s.Role.Slug + "'s memory"}
	case "why":
		rep, err := s.Why()
		if err != nil {
			return Result{Err: err}
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "turn %d via %s\n", rep.Turn, rep.Backend)
		fmt.Fprintf(&sb, "skills loaded: %s\n", orNone(strings.Join(rep.Skills, ", ")))
		fmt.Fprintf(&sb, "memory entries in context: %s\n", orNone(strings.Join(rep.MemoryIDs, ", ")))
		fmt.Fprintf(&sb, "inbox messages in context: %s\n", orNone(strings.Join(rep.InboxIDs, ", ")))
		if len(rep.Experience) == 0 {
			sb.WriteString("experience entries echoed: none detected\n")
		} else {
			sb.WriteString("experience entries echoed (via .index.json):\n")
			for _, e := range rep.Experience {
				fmt.Fprintf(&sb, "  %s — %s\n     \"%s\"\n", e.SourceID, e.Source, truncate(e.Sentence, 120))
			}
		}
		return Result{Output: strings.TrimRight(sb.String(), "\n")}
	case "skills":
		lines := s.SkillsReport()
		if len(lines) == 0 {
			return Result{Output: "no skills for " + s.Role.Slug}
		}
		return Result{Output: "* = loaded on the last turn (selected by description match)\n" + strings.Join(lines, "\n")}
	case "flag":
		if err := s.Flag(rest); err != nil {
			return Result{Err: err}
		}
		return Result{Output: "flagged the last response" + orEmpty(rest, ": "+rest)}
	case "attach":
		switch rest {
		case "", "list":
			if len(s.attachments) == 0 {
				return Result{Output: "no attachments; /attach <path>"}
			}
			var sb strings.Builder
			for _, a := range s.attachments {
				fmt.Fprintf(&sb, "  %s (%s, %d bytes)\n", a.Name, a.MediaType, len(a.Data))
			}
			return Result{Output: strings.TrimRight(sb.String(), "\n")}
		case "clear":
			n := s.Detach("")
			return Result{Output: fmt.Sprintf("removed %d attachment(s)", n)}
		}
		a, err := s.Attach(rest)
		if err != nil {
			return Result{Err: err}
		}
		msg := fmt.Sprintf("attached %s (%s, %d bytes) for this session — its contents are untrusted data to the role", a.Name, a.MediaType, len(a.Data))
		if warn := s.attachmentWarning(a); warn != "" {
			msg += "\nwarning: " + warn
		}
		return Result{Output: msg}
	case "editor":
		return Result{Action: ActOpenEditor}
	case "copy":
		n := 1
		if rest != "" {
			if v, err := strconv.Atoi(rest); err == nil && v > 0 {
				n = v
			}
		}
		text, ok := s.LastReply(n)
		if !ok {
			return Result{Err: fmt.Errorf("no reply to copy")}
		}
		how, err := CopyToClipboard(text)
		if err != nil {
			return Result{Err: err}
		}
		return Result{Output: fmt.Sprintf("copied reply (%d words) via %s", len(strings.Fields(text)), how)}
	case "undo":
		t, err := s.Undo()
		if err != nil {
			return Result{Err: err}
		}
		return Result{Output: fmt.Sprintf("removed turn %d from the active context (kept in the transcript)", t.N), Action: ActRedraw}
	case "retry":
		last, ok := s.LastUser()
		if !ok {
			return Result{Err: fmt.Errorf("nothing to retry")}
		}
		return Result{Action: ActResend, Arg: last}
	case "usage":
		var sb strings.Builder
		if s.BudgetLine != nil {
			sb.WriteString("budget   " + s.BudgetLine() + "\n")
		}
		if len(s.turns) > 0 {
			t := s.turns[len(s.turns)-1]
			if t.ContextWindow > 0 {
				fmt.Fprintf(&sb, "context  %s of %s (%.0f%%) on the last turn\n", kilo(t.InputTokens), kilo(t.ContextWindow), 100*float64(t.InputTokens)/float64(t.ContextWindow))
			} else {
				fmt.Fprintf(&sb, "context  %s input tokens on the last turn (window unknown for %s)\n", kilo(t.InputTokens), t.Backend)
			}
		}
		turns, words, _ := s.Stats()
		fmt.Fprintf(&sb, "session  %d turn(s), %d reply words in context", turns, words)
		return Result{Output: sb.String()}
	case "save":
		p := rest
		if p == "" {
			p = orDefault(s.Slug, "session") + ".md"
		}
		if strings.HasPrefix(p, "~/") {
			if h, err := os.UserHomeDir(); err == nil {
				p = h + p[1:]
			}
		}
		if err := os.WriteFile(p, []byte(s.Export()), 0o600); err != nil {
			return Result{Err: err}
		}
		return Result{Output: "saved " + p}
	case "title":
		return Dispatch(ctx, s, "/name "+rest)
	case "voice":
		switch rest {
		case "", "status":
			state := "off"
			if s.VoiceOn {
				state = "on"
			}
			if s.Voice == nil {
				return Result{Output: "voice: unavailable in this session (start with --voice, or set voice.provider to \"os\")"}
			}
			return Result{Output: "voice: " + state + " (Ctrl+B toggles; listen is a documented no-op, replies are spoken)"}
		case "on":
			if s.Voice == nil {
				return Result{Err: fmt.Errorf("no voice provider; start water with --voice")}
			}
			s.VoiceOn = true
			return Result{Output: "voice on: replies will be spoken", Action: ActRedraw}
		case "off":
			s.VoiceOn = false
			return Result{Output: "voice off", Action: ActRedraw}
		}
		return Result{Err: fmt.Errorf("usage: /voice [on|off|status]")}
	}
	return Result{Err: fmt.Errorf("unknown command /%s (try /help)", name)}
}

// ConfirmDelete performs the deletion after the UI confirmed it.
func (s *Session) ConfirmDelete(slug string) error {
	if err := s.Store.Delete(slug); err != nil {
		return err
	}
	if slug == s.Slug {
		s.Clear()
	}
	return nil
}

// OpenExternalEditor writes seed to a temp file, opens $EDITOR, and returns
// the edited text. Used by /editor and Ctrl+X.
func OpenExternalEditor(seed string) (string, error) {
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		return "", fmt.Errorf("$EDITOR is not set")
	}
	f, err := os.CreateTemp("", "water-msg-*.md")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	_ = os.Chmod(f.Name(), 0o600)
	_, _ = f.WriteString(seed)
	f.Close()
	parts := strings.Fields(ed)
	cmd := exec.Command(parts[0], append(parts[1:], f.Name())...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// ListSessions is used by the resume picker.
func ListSessions(store *session.Store) ([]session.Info, error) { return store.List() }

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func orEmpty(s, v string) string {
	if s == "" {
		return ""
	}
	return v
}

// SortedRoles returns registry slugs sorted with the orchestrator first.
func SortedRoles(slugs []string) []string {
	out := append([]string(nil), slugs...)
	sort.SliceStable(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

var _ = time.Now
