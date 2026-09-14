package experience

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"water/internal/backend"
)

func setupRole(t *testing.T) (dir, role string) {
	t.Helper()
	root := t.TempDir()
	role = "testrole"
	roleDir := filepath.Join(root, role)
	if err := os.MkdirAll(roleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exp := "---\nschema: 1\nrole: testrole\nkind: experience\nstatus: written\n---\n\n" +
		"These are lessons I carry from documented cases.\n\n" +
		"I never ship on a Friday without a rollback plan.\n"
	if err := os.WriteFile(filepath.Join(roleDir, "experience.md"), []byte(exp), 0o644); err != nil {
		t.Fatal(err)
	}
	idx := `{"schema":1,"role":"testrole","entries":[{"sentence":"I never ship on a Friday without a rollback plan.","source_id":"CASE-1","source":"seed case","confidence":1.0}]}`
	if err := os.WriteFile(filepath.Join(roleDir, ".index.json"), []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	return root, role
}

func TestGrow_New(t *testing.T) {
	dir, role := setupRole(t)
	reply := `{"action":"new","matched":"","lesson":"I double-check every migration against a staging replica before it touches production.","rationale":"feedback taught a new transferable lesson"}`
	fake := backend.NewFake("fake")
	fake.Reply = func(req backend.Request) string { return reply }

	result, err := Grow(context.Background(), fake, dir, role, "q", "r", "f", false)
	if err != nil {
		t.Fatalf("Grow: %v", err)
	}
	if result.Action != "new" {
		t.Fatalf("action = %q, want new", result.Action)
	}

	body, err := os.ReadFile(filepath.Join(dir, role, "experience.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "I double-check every migration") {
		t.Errorf("experience.md was not appended:\n%s", body)
	}

	idxBytes, err := os.ReadFile(filepath.Join(dir, role, ".index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var idx struct {
		Entries []struct{ Sentence, SourceID string } `json:"entries"`
	}
	if err := json.Unmarshal(idxBytes, &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Entries) != 2 {
		t.Fatalf("index entries = %d, want 2", len(idx.Entries))
	}

	logBytes, err := os.ReadFile(filepath.Join(dir, role, "results", "experience_growth_log.jsonl"))
	if err != nil {
		t.Fatalf("growth log not written: %v", err)
	}
	if !strings.Contains(string(logBytes), "\"action\":\"new\"") {
		t.Errorf("growth log missing action: %s", logBytes)
	}
}

func TestGrow_Reinforce(t *testing.T) {
	dir, role := setupRole(t)
	const existing = "I never ship on a Friday without a rollback plan."
	reply := `{"action":"reinforce","matched":"` + existing + `","lesson":"","rationale":"independent case confirms it"}`
	fake := backend.NewFake("fake")
	fake.Reply = func(req backend.Request) string { return reply }

	result, err := Grow(context.Background(), fake, dir, role, "q", "r", "f", false)
	if err != nil {
		t.Fatalf("Grow: %v", err)
	}
	if result.Action != "reinforce" {
		t.Fatalf("action = %q, want reinforce", result.Action)
	}

	cands, err := Candidates(dir, role, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Support != 2 || cands[0].Sentence != existing {
		t.Fatalf("candidates = %+v, want one entry with support 2", cands)
	}

	// experience.md prose must be unchanged by a reinforce.
	body, _ := os.ReadFile(filepath.Join(dir, role, "experience.md"))
	if strings.Count(string(body), existing) != 1 {
		t.Errorf("reinforce should not duplicate the lesson text in experience.md:\n%s", body)
	}
}

func TestGrow_ReinforceUnknownTarget(t *testing.T) {
	dir, role := setupRole(t)
	reply := `{"action":"reinforce","matched":"this sentence does not exist anywhere in the file","lesson":"","rationale":"x"}`
	fake := backend.NewFake("fake")
	fake.Reply = func(req backend.Request) string { return reply }

	if _, err := Grow(context.Background(), fake, dir, role, "q", "r", "f", false); err == nil {
		t.Fatal("expected an error when the reflected match isn't found verbatim")
	}
}

func TestGrow_None(t *testing.T) {
	dir, role := setupRole(t)
	reply := `{"action":"none","matched":"","lesson":"","rationale":"one-off style preference, nothing transferable"}`
	fake := backend.NewFake("fake")
	fake.Reply = func(req backend.Request) string { return reply }

	result, err := Grow(context.Background(), fake, dir, role, "q", "r", "f", false)
	if err != nil {
		t.Fatalf("Grow: %v", err)
	}
	if result.Action != "none" {
		t.Fatalf("action = %q, want none", result.Action)
	}
	if _, err := os.Stat(filepath.Join(dir, role, "results", "experience_growth_log.jsonl")); !os.IsNotExist(err) {
		t.Error("action=none must not write a growth log entry")
	}
}

func TestGrow_DryRunWritesNothing(t *testing.T) {
	dir, role := setupRole(t)
	reply := `{"action":"new","matched":"","lesson":"I always dry-run a destructive migration first.","rationale":"x"}`
	fake := backend.NewFake("fake")
	fake.Reply = func(req backend.Request) string { return reply }

	before, _ := os.ReadFile(filepath.Join(dir, role, "experience.md"))
	if _, err := Grow(context.Background(), fake, dir, role, "q", "r", "f", true); err != nil {
		t.Fatalf("Grow: %v", err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, role, "experience.md"))
	if string(before) != string(after) {
		t.Error("dry-run must not modify experience.md")
	}
}

func TestCandidates_ThresholdAndOrder(t *testing.T) {
	dir, role := setupRole(t)
	idx := `{"schema":1,"role":"testrole","entries":[
		{"sentence":"A","source_id":"s1","source":"x"},
		{"sentence":"A","source_id":"s2","source":"x"},
		{"sentence":"A","source_id":"s3","source":"x"},
		{"sentence":"B","source_id":"s4","source":"x"},
		{"sentence":"B","source_id":"s5","source":"x"},
		{"sentence":"C","source_id":"s6","source":"x"}
	]}`
	if err := os.WriteFile(filepath.Join(dir, role, ".index.json"), []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	cands, err := Candidates(dir, role, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 {
		t.Fatalf("candidates = %+v, want 2 (A and B, not C)", cands)
	}
	if cands[0].Sentence != "A" || cands[0].Support != 3 {
		t.Errorf("cands[0] = %+v, want A with support 3", cands[0])
	}
	if cands[1].Sentence != "B" || cands[1].Support != 2 {
		t.Errorf("cands[1] = %+v, want B with support 2", cands[1])
	}
}

func TestGrow_InvalidActionFromModel(t *testing.T) {
	dir, role := setupRole(t)
	fake := backend.NewFake("fake")
	fake.Reply = func(req backend.Request) string { return `{"action":"promote","matched":"","lesson":"","rationale":"x"}` }
	if _, err := Grow(context.Background(), fake, dir, role, "q", "r", "f", false); err == nil {
		t.Fatal("expected an error for an unrecognised action")
	}
}

func TestGrow_NonJSONReplyErrors(t *testing.T) {
	dir, role := setupRole(t)
	fake := backend.NewFake("fake")
	fake.Reply = func(req backend.Request) string { return "sure thing, here's my analysis: ..." }
	if _, err := Grow(context.Background(), fake, dir, role, "q", "r", "f", false); err == nil {
		t.Fatal("expected an error for a non-JSON reply")
	}
}
