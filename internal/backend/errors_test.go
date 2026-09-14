package backend

import "testing"

func TestErrorSummaryFindsTheRealError(t *testing.T) {
	codexStderr := "Reading additional input from stdin...\nOpenAI Codex v0.154.0\n--------\nmodel: bad\n--------\nuser\nhi\nwarning: Model metadata for `bad` not found.\nERROR: {\"type\":\"error\",\"status\":400,\"error\":{\"type\":\"invalid_request_error\",\"message\":\"The 'bad' model is not supported.\"}}\n"
	if got := ErrorSummary(codexStderr, ""); got != "The 'bad' model is not supported." {
		t.Fatalf("codex: %q", got)
	}
	if got := ErrorSummary("", `{"type":"system","subtype":"init"}`+"\n"+"Error: authentication failed"); got != "Error: authentication failed" {
		t.Fatalf("stream-json: %q", got)
	}
	if got := ErrorSummary("plain failure\n", ""); got != "plain failure" {
		t.Fatalf("plain: %q", got)
	}
	if got := redactArgs([]string{"--print", "--system-prompt", "SECRET PERSONA", "--tools", "", "--strict-mcp-config", "--", "PROMPT TEXT"}); got != `--print --system-prompt <14 bytes> --tools "" --strict-mcp-config -- <prompt 11 bytes>` {
		t.Fatalf("redact: %q", got)
	}
}
