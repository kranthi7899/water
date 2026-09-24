package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"water/internal/connectors"
	"water/internal/gate/permit"
	"water/internal/store"
	"water/internal/twins"
)

// ---- github ----
//
// A demo-only, in-memory stand-in for a GitHub connector: no network call,
// no OAuth, no API token, ever. It exists so the twin can be demonstrated
// pulling engineering signal (pull requests, issues) alongside the real
// Google connectors, without the owner setting up a real GitHub App for a
// repo they may not even have wired up yet.

// githubRepo is the one fictional repo every seeded PR/issue belongs to.
const githubRepo = "nimbus-labs/core-api"

// GitHubPR is one fake pull request.
type GitHubPR struct {
	ID           string   `json:"id"`
	Number       int      `json:"number"`
	Title        string   `json:"title"`
	Author       string   `json:"author"`
	State        string   `json:"state"` // open | closed | merged
	ReviewStatus string   `json:"review_status,omitempty"`
	Reviewers    []string `json:"reviewers,omitempty"`
	URL          string   `json:"url"`
	UpdatedAt    string   `json:"updated_at"` // RFC 3339
}

// GitHubIssue is one fake issue.
type GitHubIssue struct {
	ID        string   `json:"id"`
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	Author    string   `json:"author"`
	State     string   `json:"state"` // open | closed
	Assignee  string   `json:"assignee,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	URL       string   `json:"url"`
	UpdatedAt string   `json:"updated_at"` // RFC 3339
}

// DefaultGitHubPRs is the canonical demo seed: a handful of realistic,
// clearly-fictional pull requests for a small startup's API repo.
func DefaultGitHubPRs() []GitHubPR {
	return []GitHubPR{
		{ID: "pr-482", Number: 482, Title: "Fix race in payment webhook retry", Author: "priya-k", State: "merged", ReviewStatus: "approved", Reviewers: []string{"dgoldberg"}, URL: "https://github.com/" + githubRepo + "/pull/482", UpdatedAt: "2026-09-20T14:32:00Z"},
		{ID: "pr-486", Number: 486, Title: "Add rate limiting to public API", Author: "jsong", State: "open", ReviewStatus: "changes_requested", Reviewers: []string{"priya-k"}, URL: "https://github.com/" + githubRepo + "/pull/486", UpdatedAt: "2026-09-23T09:05:00Z"},
		{ID: "pr-479", Number: 479, Title: "Upgrade Postgres client to v5", Author: "mrivera", State: "closed", ReviewStatus: "pending", URL: "https://github.com/" + githubRepo + "/pull/479", UpdatedAt: "2026-09-18T11:00:00Z"},
		{ID: "pr-490", Number: 490, Title: "Retry logic for flaky S3 uploads", Author: "dgoldberg", State: "open", ReviewStatus: "pending", Reviewers: []string{"jsong"}, URL: "https://github.com/" + githubRepo + "/pull/490", UpdatedAt: "2026-09-24T08:15:00Z"},
	}
}

// DefaultGitHubIssues is the canonical demo seed of issues.
func DefaultGitHubIssues() []GitHubIssue {
	return []GitHubIssue{
		{ID: "issue-201", Number: 201, Title: "Webhook retries duplicate charges under load", Author: "dana@acme.example", State: "open", Assignee: "priya-k", Labels: []string{"bug", "payments", "p1"}, URL: "https://github.com/" + githubRepo + "/issues/201", UpdatedAt: "2026-09-21T16:40:00Z"},
		{ID: "issue-205", Number: 205, Title: "API rate limit headers missing from docs", Author: "jsong", State: "open", Assignee: "jsong", Labels: []string{"docs"}, URL: "https://github.com/" + githubRepo + "/issues/205", UpdatedAt: "2026-09-22T10:00:00Z"},
		{ID: "issue-198", Number: 198, Title: "Investigate flaky S3 upload test in CI", Author: "dgoldberg", State: "open", Assignee: "dgoldberg", Labels: []string{"ci", "flaky-test"}, URL: "https://github.com/" + githubRepo + "/issues/198", UpdatedAt: "2026-09-19T13:20:00Z"},
		{ID: "issue-190", Number: 190, Title: "Add pagination to /v1/customers", Author: "mrivera", State: "closed", Assignee: "mrivera", Labels: []string{"enhancement"}, URL: "https://github.com/" + githubRepo + "/issues/190", UpdatedAt: "2026-09-10T09:00:00Z"},
	}
}

// GitHub is the fake connector.
type GitHub struct {
	mu     sync.Mutex
	prs    []GitHubPR
	issues []GitHubIssue
}

// NewGitHub builds the connector from explicit seed data (tests pass their
// own; production demo wiring passes DefaultGitHubPRs()/DefaultGitHubIssues()).
func NewGitHub(prs []GitHubPR, issues []GitHubIssue) *GitHub {
	return &GitHub{prs: prs, issues: issues}
}

func (*GitHub) Name() string                 { return "github" }
func (*GitHub) Credential() (string, string) { return "", "" }

func (*GitHub) Functions() []connectors.Function {
	return []connectors.Function{
		{Name: "list_prs", Description: "List pull requests on the demo repo.", Level: twins.R, Risk: connectors.RiskLow, External: true,
			Schema: connectors.Schema{Properties: map[string]connectors.Property{"state": str("filter: open, closed or merged")}}},
		{Name: "list_issues", Description: "List issues on the demo repo.", Level: twins.R, Risk: connectors.RiskLow, External: true,
			Schema: connectors.Schema{Properties: map[string]connectors.Property{"state": str("filter: open or closed"), "assignee": str("filter by assignee login")}}},
	}
}

func (g *GitHub) Invoke(_ context.Context, p permit.Permit) (json.RawMessage, error) {
	call, err := p.Open()
	if err != nil {
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	switch call.Function {
	case "list_prs":
		state := strings.ToLower(arg(call.Args, "state"))
		var out []GitHubPR
		for _, pr := range g.prs {
			if state != "" && strings.ToLower(pr.State) != state {
				continue
			}
			out = append(out, pr)
		}
		return json.Marshal(out)
	case "list_issues":
		state := strings.ToLower(arg(call.Args, "state"))
		assignee := strings.ToLower(arg(call.Args, "assignee"))
		var out []GitHubIssue
		for _, is := range g.issues {
			if state != "" && strings.ToLower(is.State) != state {
				continue
			}
			if assignee != "" && strings.ToLower(is.Assignee) != assignee {
				continue
			}
			out = append(out, is)
		}
		return json.Marshal(out)
	}
	return nil, fmt.Errorf("github: unknown function %q", call.Function)
}

// Normalize maps PRs onto store.Commit (the closest shape to "a change,
// with an author, a message and a repo" the store offers) and issues onto
// store.Issue. Both are External: true — this is other people's activity,
// not the CEO's own.
func (g *GitHub) Normalize(fn string, raw json.RawMessage) ([]store.Record, error) {
	switch fn {
	case "list_prs":
		var prs []GitHubPR
		if err := json.Unmarshal(raw, &prs); err != nil {
			return nil, err
		}
		var out []store.Record
		for _, pr := range prs {
			at, _ := time.Parse(time.RFC3339, pr.UpdatedAt)
			msg := fmt.Sprintf("PR #%d: %s [%s", pr.Number, pr.Title, pr.State)
			if pr.ReviewStatus != "" {
				msg += ", review " + pr.ReviewStatus
			}
			msg += "]"
			out = append(out, &store.Commit{
				Meta:        store.Meta{Source: g.Name(), SourceID: pr.ID, External: true},
				Repo:        githubRepo,
				SHA:         fmt.Sprintf("pr-%d", pr.Number),
				Author:      pr.Author,
				Message:     msg,
				CommittedAt: at.UTC(),
				URL:         pr.URL,
			})
		}
		return out, nil
	case "list_issues":
		var issues []GitHubIssue
		if err := json.Unmarshal(raw, &issues); err != nil {
			return nil, err
		}
		var out []store.Record
		for _, is := range issues {
			out = append(out, &store.Issue{
				Meta:     store.Meta{Source: g.Name(), SourceID: is.ID, External: true},
				Title:    fmt.Sprintf("#%d %s", is.Number, is.Title),
				State:    is.State,
				Assignee: is.Assignee,
				Project:  githubRepo,
				Priority: strings.Join(is.Labels, ","),
				URL:      is.URL,
			})
		}
		return out, nil
	}
	return nil, nil
}
