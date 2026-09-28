package fake_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"water/internal/connectors/fake"
	"water/internal/gate"
	"water/internal/store"
)

const githubManifest = `
id: t
name: T
usage: {window: 1h, model_calls: 10}
connectors:
  - name: github
    functions:
      - {name: list_prs, level: R}
      - {name: list_issues, level: R}
`

func TestGitHubFunctionsSchema(t *testing.T) {
	gh := fake.NewGitHub(fake.DefaultGitHubPRs(), fake.DefaultGitHubIssues())
	for _, f := range gh.Functions() {
		if f.Level != "R" {
			t.Fatalf("%s: level %s, want R (read-only demo connector)", f.Name, f.Level)
		}
		if !f.External {
			t.Fatalf("%s: must be marked External (other people's activity)", f.Name)
		}
		b, err := json.Marshal(f.Schema)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), `"additionalProperties":false`) {
			t.Fatalf("%s schema: %s", f.Name, b)
		}
		if err := f.Schema.Validate(map[string]any{"state": "open"}); err != nil {
			t.Fatalf("%s: valid args rejected: %v", f.Name, err)
		}
		if err := f.Schema.Validate(map[string]any{"bogus": "x"}); err == nil {
			t.Fatalf("%s: unknown argument accepted", f.Name)
		}
	}
}

func TestGitHubNormalizePRsAndIssues(t *testing.T) {
	gh := fake.NewGitHub(fake.DefaultGitHubPRs(), fake.DefaultGitHubIssues())

	prRaw, err := json.Marshal(fake.DefaultGitHubPRs()[:1])
	if err != nil {
		t.Fatal(err)
	}
	recs, err := gh.Normalize("list_prs", prRaw)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("normalize list_prs: %d records, want 1", len(recs))
	}
	c, ok := recs[0].(*store.Commit)
	if !ok {
		t.Fatalf("PR did not normalize to *store.Commit: %T", recs[0])
	}
	if !c.External || c.Author != "priya-k" || c.Repo == "" || c.SHA == "" || !strings.Contains(c.Message, "Fix race in payment webhook retry") {
		t.Fatalf("commit from PR: %+v", c)
	}

	issueRaw, err := json.Marshal(fake.DefaultGitHubIssues()[:1])
	if err != nil {
		t.Fatal(err)
	}
	recs, err = gh.Normalize("list_issues", issueRaw)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("normalize list_issues: %d records, want 1", len(recs))
	}
	is, ok := recs[0].(*store.Issue)
	if !ok {
		t.Fatalf("issue did not normalize to *store.Issue: %T", recs[0])
	}
	if !is.External || is.Assignee != "priya-k" || is.State != "open" || is.URL == "" {
		t.Fatalf("issue: %+v", is)
	}
}

func TestGitHubInvokeThroughTheGate(t *testing.T) {
	gh := fake.NewGitHub(fake.DefaultGitHubPRs(), fake.DefaultGitHubIssues())
	g := newGateHarness(t, githubManifest, gh)

	res, err := g.Invoke(context.Background(), gate.Call{Function: "github.list_prs", Origin: gate.P1, Taint: gate.Clean})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Untrusted {
		t.Fatal("github.list_prs result must be marked untrusted (External)")
	}
	if len(res.Records) != len(fake.DefaultGitHubPRs()) {
		t.Fatalf("records: %d, want %d", len(res.Records), len(fake.DefaultGitHubPRs()))
	}

	res, err = g.Invoke(context.Background(), gate.Call{Function: "github.list_issues", Args: map[string]any{"state": "open"}, Origin: gate.P1, Taint: gate.Clean})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res.Records {
		is := r.(*store.Issue)
		if is.State != "open" {
			t.Fatalf("state filter leaked a %s issue: %+v", is.State, is)
		}
	}
	if len(res.Records) == 0 {
		t.Fatal("expected at least one open issue")
	}
}
