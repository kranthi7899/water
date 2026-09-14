package chat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"water/internal/backend"
)

// TestSlashPathIsNotACommand — only a known command name makes a line a
// command; dropped paths and prose that starts with "/" do not.
func TestSlashPathIsNotACommand(t *testing.T) {
	for _, in := range []string{"/help", "/attach x.pdf", "/q", "/exit", "/consult cto is this feasible?", "  /why  "} {
		if !IsCommand(in) {
			t.Fatalf("%q should be a command", in)
		}
	}
	for _, in := range []string{"/Users/k/Downloads/report.pdf", "/etc/hosts has two entries, why?", "/r/golang says Go is fast", "/", "/helpme", "hello"} {
		if IsCommand(in) {
			t.Fatalf("%q must not be treated as a command", in)
		}
	}
	// A pasted multi-line block whose first line starts with "/" is text.
	if IsCommand("/var/log/app.log shows:\nERROR timeout\nERROR retry") {
		t.Fatal("pasted log block treated as a command")
	}
}

// TestDroppedPathNormalised — terminals paste dragged files quoted or with
// backslash-escaped spaces; both resolve to the real file.
func TestDroppedPathNormalised(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "Board Memo (Q3).pdf")
	os.WriteFile(p, []byte("%PDF-1.4"), 0o644)
	escaped := strings.NewReplacer(" ", `\ `, "(", `\(`, ")", `\)`).Replace(p)
	for _, in := range []string{p, "'" + p + "'", `"` + p + `"`, escaped, "  " + escaped + "  "} {
		got, ok := DroppedPath(in)
		if !ok || got != p {
			t.Fatalf("%q -> %q %v", in, got, ok)
		}
	}
	if _, ok := DroppedPath("/etc/hosts has entries"); ok {
		t.Fatal("prose is not a dropped path")
	}
	if a, err := LoadAttachment(escaped); err != nil || a.Kind != "document" {
		t.Fatalf("attach escaped path: %+v %v", a, err)
	}
}

// TestAtPathFailureIsLoud — an @path that cannot be loaded stops the turn
// with a reason instead of silently sending the message without the file.
func TestAtPathFailureIsLoud(t *testing.T) {
	s, fake := newSession(t, func(req backend.Request) string { return "ok" })
	_, err := s.Send(context.Background(), "summarise @/nope/missing.pdf please")
	if err == nil || !strings.Contains(err.Error(), "could not attach @/nope/missing.pdf") {
		t.Fatalf("missing @path: %v", err)
	}
	if fake.Calls() != 0 {
		t.Fatal("the model was called without the attachment")
	}
	// A non-path mention is ordinary text.
	if _, err := s.Send(context.Background(), "ask @alice about it"); err != nil {
		t.Fatalf("@mention: %v", err)
	}
}

// TestDroppedFilePrefillsAttach — pressing Enter on a bare dropped path puts
// "/attach <path>" in the input instead of failing or sending the path.
func TestDroppedFilePrefillsAttach(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 100, 30
	if err := m.enterRole("ceo", ""); err != nil {
		t.Fatal(err)
	}
	m.relayout()
	p := filepath.Join(t.TempDir(), "memo.pdf")
	os.WriteFile(p, []byte("%PDF-1.4"), 0o644)
	m.ed.SetValue(p)
	_, cmd := m.updateChat(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("a dropped path started a turn or command")
	}
	if v := m.ed.Value(); v != "/attach "+p {
		t.Fatalf("input after enter: %q", v)
	}
	if !strings.Contains(m.status, "looks like a file") {
		t.Fatalf("status: %q", m.status)
	}
}

// TestUnsupportedAttachmentRefused — a backend that cannot read a PDF refuses
// loudly; it no longer drops the file and lets the model say it cannot see it.
func TestUnsupportedAttachmentRefused(t *testing.T) {
	c := &backend.CodexSubscription{Bin: "definitely-not-installed-codex"}
	if c.SupportsDocuments() {
		t.Fatal("codex claims document support")
	}
	api := &backend.API{Key: "k", BaseURL: "http://127.0.0.1:1"}
	_, err := api.Run(context.Background(), backend.Request{Prompt: "x", Attachments: []backend.Attachment{{Name: "memo.pdf", Kind: "document", MediaType: "application/pdf"}}})
	if !errors.Is(err, backend.ErrAttachmentUnsupported) || !strings.Contains(err.Error(), "claude-subscription") {
		t.Fatalf("api with pdf: %v", err)
	}
}
