package backend

import (
	"context"
	"os"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"testing"
)

// TestCodexKeepsInlinedAttachmentsWithASystemPrompt: a text attachment must
// reach the model even when the request has a system prompt (every twin
// turn has one), since AttachmentsDelivered then reports it as consumed.
func TestCodexKeepsInlinedAttachmentsWithASystemPrompt(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "codex")
	// `codex exec --help` advertises nothing optional; a run echoes its
	// final argument (the prompt) to stdout, which becomes resp.Text.
	script := "#!/bin/sh\n" +
		"if [ \"$2\" = \"--help\" ]; then echo 'Usage: codex exec'; exit 0; fi\n" +
		"for a; do last=\"$a\"; done\n" +
		"printf '%s' \"$last\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	c := &CodexSubscription{Bin: bin}
	resp, err := c.Run(context.Background(), Request{
		System: "sys", Prompt: "the prompt",
		Attachments: []Attachment{{Name: "notes.txt", MediaType: "text/plain", Kind: "text", Data: []byte("ATTACHMENT-BODY")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.AttachmentsDelivered {
		t.Fatal("AttachmentsDelivered = false, want true")
	}
	for _, want := range []string{"sys", "the prompt", "ATTACHMENT-BODY"} {
		if !strings.Contains(resp.Text, want) {
			t.Fatalf("prompt sent to codex = %q, missing %q", resp.Text, want)
		}
	}
}
