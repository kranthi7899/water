// Package chat is the interactive single-role session: transcript-backed
// turns, slash commands (Part 4.2), attachments (5.4), and the Bubble Tea
// program that renders it inside the Part 7 layout.
//
// The core (Session, Dispatch) has no terminal dependency so every command is
// testable without a TTY; the TUI in tui.go is a thin renderer over it.
package chat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"water/internal/agent"
	"water/internal/backend"
	"water/internal/memory"
	"water/internal/orchestrator"
	"water/internal/persona"
	"water/internal/roles"
	"water/internal/session"
)

// Turn is one completed exchange kept in the active context.
type Turn struct {
	N         int
	User      string
	Reply     string
	Prompt    agent.Prompt
	Backend   string
	Model     string
	Duration  time.Duration
	At        time.Time
	Flagged   bool
	Untrusted bool
}

// Session is the live state of one role conversation.
type Session struct {
	Registry *roles.Registry
	Role     *roles.Role
	Env      agent.Env
	Store    *session.Store
	Slug     string
	Name     string
	Pinned   bool

	// Backend resolution for /backend and /status.
	BackendName   string
	BackendReason string
	SwitchBackend func(name string) (backend.Backend, string, error)

	// Retention applied when a new session is created.
	Retention session.Retention

	// Summariser produces compaction summaries (nil = extractive fallback).
	Summariser func(ctx context.Context, c *session.Context, focus string) (string, error)

	Voice func(text string) error // optional TTS hook (3D)

	turns       []Turn
	summary     string
	consults    []orchestrator.AgentMessage
	attachments []backend.Attachment
	history     []string
	histIdx     int
	lastSkills  []string
}

// Open starts or resumes a session for role. slug "" creates a new one.
func Open(reg *roles.Registry, role *roles.Role, env agent.Env, store *session.Store, slug string) (*Session, error) {
	s := &Session{Registry: reg, Role: role, Env: env, Store: store}
	if slug == "" {
		return s, nil // created lazily on the first turn so /clear leaves no empty files
	}
	if err := s.resume(slug); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Session) resume(slug string) error {
	c, err := s.Store.Open(slug)
	if err != nil {
		return err
	}
	s.Slug, s.Name, s.Pinned = slug, c.Name, c.Pinned
	s.turns, s.summary, s.consults = nil, "", nil
	if c.Summary != nil {
		s.summary = c.Summary.Text
	}
	var cur *Turn
	for _, e := range c.Entries {
		switch e.Kind {
		case session.KindUser:
			cur = &Turn{N: e.Turn, User: e.Text, At: e.At}
		case session.KindAssistant:
			if cur == nil {
				cur = &Turn{N: e.Turn}
			}
			cur.Reply, cur.Backend = e.Text, e.Backend
			cur.Prompt.Skills, cur.Prompt.MemoryIDs, cur.Prompt.InboxIDs = e.Skills, e.MemoryIDs, e.InboxIDs
			s.turns = append(s.turns, *cur)
			cur = nil
		case session.KindConsult:
			s.consults = append(s.consults, orchestrator.AgentMessage{ID: "consult-" + e.Role, From: e.Role, To: s.Role.Slug, Topic: orchestrator.TopicAnswer, Payload: e.Text, At: e.At})
		case session.KindFlag:
			for i := range s.turns {
				if s.turns[i].N == e.Turn {
					s.turns[i].Flagged = true
				}
			}
		}
	}
	return nil
}

// Turns returns the active context turns.
func (s *Session) Turns() []Turn { return append([]Turn(nil), s.turns...) }

// Summary returns the active compaction summary, if any.
func (s *Session) Summary() string { return s.summary }

// Attachments returns session-scoped attachments.
func (s *Session) Attachments() []backend.Attachment { return s.attachments }

func (s *Session) ensureSession(first string) error {
	if s.Slug != "" {
		return nil
	}
	s.Slug = s.Store.Slugify(first, time.Now())
	if err := s.Store.Create(s.Slug, ""); err != nil {
		return err
	}
	if s.Retention.Keep > 0 || s.Retention.MaxAge > 0 {
		_, _ = s.Store.Prune(s.Retention, time.Now())
	}
	return nil
}

// prior renders the active context as inbox messages addressed to the role:
// the compaction summary, its own earlier turns, and /consult answers. Only
// this role's own transcript ever enters here.
func (s *Session) prior() []orchestrator.AgentMessage {
	var out []orchestrator.AgentMessage
	if s.summary != "" {
		out = append(out, orchestrator.AgentMessage{ID: "summary", From: s.Role.Slug, To: s.Role.Slug, Topic: "summary", Payload: "Earlier in this conversation (compacted): " + s.summary})
	}
	for _, t := range s.turns {
		out = append(out, orchestrator.AgentMessage{ID: fmt.Sprintf("turn-%d-user", t.N), From: orchestrator.UserSender, To: s.Role.Slug, Topic: orchestrator.TopicBrief, Payload: t.User, At: t.At})
		out = append(out, orchestrator.AgentMessage{ID: fmt.Sprintf("turn-%d-reply", t.N), From: s.Role.Slug, To: s.Role.Slug, Topic: "reply", Payload: t.Reply, At: t.At})
	}
	out = append(out, s.consults...)
	return out
}

// Send runs one turn and appends it to the transcript.
func (s *Session) Send(ctx context.Context, text string) (Turn, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Turn{}, errors.New("empty message")
	}
	if err := s.ensureSession(text); err != nil {
		return Turn{}, err
	}
	s.history = append(s.history, text)
	s.histIdx = len(s.history)
	n := len(s.turns) + 1
	if len(s.turns) > 0 {
		n = s.turns[len(s.turns)-1].N + 1
	}
	prompt, atts := s.expandAtRefs(text)
	atts = append(atts, s.attachments...)
	_ = s.Store.Append(s.Slug, session.Entry{Kind: session.KindUser, Turn: n, Text: text})
	start := time.Now()
	resp, p, err := agent.RunTurn(ctx, s.Role, s.Env, s.prior(), prompt, atts)
	if err != nil {
		_ = s.Store.Append(s.Slug, session.Entry{Kind: session.KindSystem, Turn: n, Text: "error: " + err.Error()})
		return Turn{}, err
	}
	// Water marks the turn untrusted whenever external content was handed to
	// the model, whether or not the backend reports delivery (5.5).
	t := Turn{N: n, User: text, Reply: resp.Text, Prompt: p, Backend: resp.Backend, Model: resp.Model, Duration: time.Since(start), At: start, Untrusted: resp.ConsumedUntrusted() || len(atts) > 0}
	s.turns = append(s.turns, t)
	s.lastSkills = p.Skills
	_ = s.Store.Append(s.Slug, session.Entry{Kind: session.KindAssistant, Turn: n, Text: resp.Text, Backend: resp.Backend, Skills: p.Skills, MemoryIDs: p.MemoryIDs, InboxIDs: p.InboxIDs})
	if n%session.AutoCompactEvery == 0 {
		_, _ = s.Compact(ctx, "")
	}
	if s.Voice != nil {
		_ = s.Voice(resp.Text)
	}
	return t, nil
}

// expandAtRefs turns @path tokens into per-turn attachments (5.4).
func (s *Session) expandAtRefs(text string) (string, []backend.Attachment) {
	var atts []backend.Attachment
	fields := strings.Fields(text)
	for _, f := range fields {
		if !strings.HasPrefix(f, "@") || len(f) < 2 {
			continue
		}
		p := strings.Trim(f[1:], ",.;:)")
		a, err := LoadAttachment(p)
		if err != nil {
			continue
		}
		atts = append(atts, a)
	}
	return text, atts
}

// MaxAttachmentBytes caps a single attachment.
const MaxAttachmentBytes = 8 * 1024 * 1024

// LoadAttachment reads a file into an Attachment, classifying it by extension.
func LoadAttachment(p string) (backend.Attachment, error) {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(h, p[2:])
		}
	}
	st, err := os.Stat(p)
	if err != nil {
		return backend.Attachment{}, err
	}
	if st.Size() > MaxAttachmentBytes {
		return backend.Attachment{}, fmt.Errorf("%s is %d bytes; limit is %d", p, st.Size(), MaxAttachmentBytes)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return backend.Attachment{}, err
	}
	a := backend.Attachment{Name: filepath.Base(p), Data: b}
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png":
		a.Kind, a.MediaType = "image", "image/png"
	case ".jpg", ".jpeg":
		a.Kind, a.MediaType = "image", "image/jpeg"
	case ".gif":
		a.Kind, a.MediaType = "image", "image/gif"
	case ".webp":
		a.Kind, a.MediaType = "image", "image/webp"
	case ".pdf":
		a.Kind, a.MediaType = "document", "application/pdf"
	default:
		a.Kind, a.MediaType = "text", "text/plain"
		if !isText(b) {
			return backend.Attachment{}, fmt.Errorf("%s is binary and not an image/PDF", p)
		}
	}
	return a, nil
}

func isText(b []byte) bool {
	n := len(b)
	if n > 4096 {
		n = 4096
	}
	for _, c := range b[:n] {
		if c == 0 {
			return false
		}
	}
	return true
}

// Attach adds a session-scoped attachment.
func (s *Session) Attach(p string) (backend.Attachment, error) {
	a, err := LoadAttachment(p)
	if err != nil {
		return a, err
	}
	s.attachments = append(s.attachments, a)
	if s.Slug != "" {
		_ = s.Store.Append(s.Slug, session.Entry{Kind: session.KindAttach, Text: a.Name, Meta: map[string]string{"kind": a.Kind, "media_type": a.MediaType}})
	}
	return a, nil
}

// Detach removes session attachments (all, or by name).
func (s *Session) Detach(name string) int {
	if name == "" {
		n := len(s.attachments)
		s.attachments = nil
		return n
	}
	var keep []backend.Attachment
	removed := 0
	for _, a := range s.attachments {
		if a.Name == name {
			removed++
			continue
		}
		keep = append(keep, a)
	}
	s.attachments = keep
	return removed
}

// Clear resets the active context without touching the transcript file; the
// next message starts a new session (the old one stays resumable).
func (s *Session) Clear() {
	s.turns, s.summary, s.consults, s.attachments = nil, "", nil, nil
	s.Slug, s.Name, s.Pinned = "", "", false
}

// Compact appends a summary checkpoint for the active context.
func (s *Session) Compact(ctx context.Context, focus string) (string, error) {
	if s.Slug == "" {
		return "", errors.New("nothing to compact yet")
	}
	c, err := s.Store.Compact(s.Slug, focus, func(c *session.Context, focus string) (string, error) {
		if s.Summariser != nil {
			return s.Summariser(ctx, c, focus)
		}
		return extractiveSummary(c, focus), nil
	})
	if err != nil {
		return "", err
	}
	s.summary = c.Summary.Text
	s.turns = nil
	return s.summary, nil
}

// extractiveSummary is the no-model fallback: first lines of each turn.
func extractiveSummary(c *session.Context, focus string) string {
	var sb strings.Builder
	if c.Summary != nil {
		sb.WriteString(c.Summary.Text + "\n")
	}
	if focus != "" {
		sb.WriteString("Focus: " + focus + "\n")
	}
	for _, e := range c.Entries {
		if e.Kind != session.KindUser && e.Kind != session.KindAssistant {
			continue
		}
		line := strings.SplitN(strings.TrimSpace(e.Text), "\n", 2)[0]
		if len(line) > 160 {
			line = line[:160] + "…"
		}
		sb.WriteString(fmt.Sprintf("- %s: %s\n", e.Kind, line))
	}
	return strings.TrimSpace(sb.String())
}

// ModelSummariser builds a Summariser that asks the role's own backend.
func ModelSummariser(role *roles.Role, env agent.Env) func(ctx context.Context, c *session.Context, focus string) (string, error) {
	return func(ctx context.Context, c *session.Context, focus string) (string, error) {
		var sb strings.Builder
		if c.Summary != nil {
			sb.WriteString("Previous summary:\n" + c.Summary.Text + "\n\n")
		}
		for _, e := range c.Entries {
			if e.Kind == session.KindUser || e.Kind == session.KindAssistant {
				sb.WriteString(e.Kind + ": " + e.Text + "\n\n")
			}
		}
		task := "Summarise the conversation below for your own future context: decisions, open questions, facts established, and what the user cares about. Be faithful and compact."
		if focus != "" {
			task += " Focus on: " + focus + "."
		}
		resp, err := env.BackendFor(role.Slug).Run(ctx, backend.Request{System: "You are compacting your own conversation transcript. Output only the summary.", Prompt: task + "\n\n" + sb.String(), Role: role.Slug, Timeout: env.Timeout})
		if err != nil {
			return "", err
		}
		return resp.Text, nil
	}
}

// Remember promotes a note (or the last reply) into this role's curated
// memory. Manual by design; transcripts never auto-promote.
func (s *Session) Remember(ctx context.Context, note string) (memory.Entry, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		if len(s.turns) == 0 {
			return memory.Entry{}, errors.New("nothing to remember yet")
		}
		note = s.turns[len(s.turns)-1].Reply
	}
	e := memory.Entry{ID: memory.NewID(), Text: note, CreatedAt: time.Now().UTC(), Tags: []string{"remember"}}
	if err := s.Role.Memory().Add(ctx, e); err != nil {
		return memory.Entry{}, err
	}
	if s.Slug != "" {
		_ = s.Store.Append(s.Slug, session.Entry{Kind: session.KindSignal, Text: "promoted to memory " + e.ID})
	}
	return e, nil
}

// Flag marks the last response bad for the feedback loop.
func (s *Session) Flag(reason string) error {
	if len(s.turns) == 0 {
		return errors.New("no response to flag")
	}
	t := &s.turns[len(s.turns)-1]
	t.Flagged = true
	return s.Store.Append(s.Slug, session.Entry{Kind: session.KindFlag, Turn: t.N, Text: reason})
}

// Consult asks another role; the answer is added to the active context as an
// outbox message (never a memory read) and journaled.
func (s *Session) Consult(ctx context.Context, target string, question string) (orchestrator.AgentMessage, error) {
	r, ok := s.Registry.Get(target)
	if !ok {
		return orchestrator.AgentMessage{}, fmt.Errorf("unknown role %q (known: %s)", target, strings.Join(s.Registry.Slugs(), ", "))
	}
	if r.Slug == s.Role.Slug {
		return orchestrator.AgentMessage{}, errors.New("you are already talking to that role")
	}
	if err := s.ensureSession("consult " + target + " " + question); err != nil {
		return orchestrator.AgentMessage{}, err
	}
	ans, _, err := agent.Consult(ctx, s.Role.Slug, r, s.Env, question)
	if err != nil {
		return ans, err
	}
	ans.ID = fmt.Sprintf("consult-%s-%d", r.Slug, len(s.consults)+1)
	s.consults = append(s.consults, ans)
	_ = s.Store.Append(s.Slug, session.Entry{Kind: session.KindConsult, Role: r.Slug, Text: ans.Payload, Meta: map[string]string{"question": question}})
	return ans, nil
}

// WhyReport is the traceability record for the last response.
type WhyReport struct {
	Turn       int
	Skills     []string
	MemoryIDs  []string
	InboxIDs   []string
	Experience []persona.IndexEntry
	Backend    string
}

// Why explains the last response: skills loaded, memory and inbox messages
// that informed it, and experience entries whose lessons its text echoes,
// resolved through the hidden .index.json.
func (s *Session) Why() (WhyReport, error) {
	if len(s.turns) == 0 {
		return WhyReport{}, errors.New("no response yet")
	}
	t := s.turns[len(s.turns)-1]
	rep := WhyReport{Turn: t.N, Skills: t.Prompt.Skills, MemoryIDs: t.Prompt.MemoryIDs, InboxIDs: t.Prompt.InboxIDs, Backend: t.Backend}
	if s.Role.Persona != nil && s.Role.Persona.Index != nil {
		seen := map[string]bool{}
		for _, e := range s.Role.Persona.Index.Entries {
			if seen[e.SourceID] {
				continue
			}
			if echoes(t.Reply, e.Sentence) {
				seen[e.SourceID] = true
				rep.Experience = append(rep.Experience, e)
			}
		}
	}
	return rep, nil
}

// echoes reports whether reply shares enough distinctive vocabulary with an
// experience sentence to plausibly have drawn on it.
func echoes(reply, sentence string) bool {
	terms := map[string]bool{}
	for _, w := range strings.Fields(strings.ToLower(sentence)) {
		w = strings.Trim(w, ".,;:!?()\"'—-")
		if len(w) >= 7 {
			terms[w] = true
		}
	}
	if len(terms) < 4 {
		return false
	}
	lr := strings.ToLower(reply)
	hit := 0
	for w := range terms {
		if strings.Contains(lr, w) {
			hit++
		}
	}
	return float64(hit)/float64(len(terms)) >= 0.35
}

// SkillsReport lists skills loaded on the last turn and why.
func (s *Session) SkillsReport() []string {
	var out []string
	if s.Role.Persona == nil {
		return out
	}
	loaded := map[string]bool{}
	for _, sk := range s.lastSkills {
		loaded[sk] = true
	}
	for _, sk := range s.Role.Persona.Skills {
		mark := "  "
		if loaded[sk.Slug] {
			mark = "* "
		}
		out = append(out, fmt.Sprintf("%s%-32s %s", mark, sk.Slug, truncate(sk.Description, 90)))
	}
	sort.Strings(out)
	return out
}

// History navigation for the editor.
func (s *Session) HistoryPrev() (string, bool) {
	if s.histIdx <= 0 {
		return "", false
	}
	s.histIdx--
	return s.history[s.histIdx], true
}

func (s *Session) HistoryNext() (string, bool) {
	if s.histIdx >= len(s.history)-1 {
		s.histIdx = len(s.history)
		return "", s.histIdx == len(s.history)
	}
	s.histIdx++
	return s.history[s.histIdx], true
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
