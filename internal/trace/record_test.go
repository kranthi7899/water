package trace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRecordCallWritesReplayableInput — every node call is recorded with the
// exact system and prompt, numbered in order, and linked from the trace.
func TestRecordCallWritesReplayableInput(t *testing.T) {
	dir := t.TempDir()
	r, err := New(dir, "run-1")
	if err != nil {
		t.Fatal(err)
	}
	s1 := r.RecordCall(CallRecord{Role: "ceo", Backend: "fake", System: "SYS-CEO", Prompt: "PROMPT-CEO", Response: "decide"})
	s2 := r.RecordCall(CallRecord{Role: "cto", Backend: "fake", System: "SYS-CTO", Prompt: "PROMPT-CTO", Error: "boom"})
	r.Finish()
	if s1 != 1 || s2 != 2 {
		t.Fatalf("sequence %d %d", s1, s2)
	}
	b, err := os.ReadFile(filepath.Join(CallsDirFor(dir, "run-1"), "002-cto.json"))
	if err != nil {
		t.Fatal(err)
	}
	var rec CallRecord
	if json.Unmarshal(b, &rec) != nil || rec.System != "SYS-CTO" || rec.Prompt != "PROMPT-CTO" || rec.Error != "boom" || rec.RunID != "run-1" {
		t.Fatalf("record: %+v", rec)
	}
	fi, _ := os.Stat(filepath.Join(CallsDirFor(dir, "run-1"), "002-cto.json"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("call record permissions %v; it holds persona and memory text", fi.Mode().Perm())
	}
	tr, _ := os.ReadFile(filepath.Join(dir, "run-1.jsonl"))
	if strings.Count(string(tr), `"call_recorded"`) != 2 {
		t.Fatal("trace does not link the call records")
	}
}
