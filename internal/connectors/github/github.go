// Package github is a real, read-only GitHub connector: it lists pull
// requests and issues on one configured repo through GitHub's REST API,
// authenticated with a personal access token (no OAuth app needed). Its
// function names and Normalize output shapes match
// internal/connectors/fake.GitHub exactly, so anything already built against
// the demo fake (twins/ceo-demo, its investor_request card, tests) works
// unchanged against this real connector once a token is configured.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"water/internal/connectors"
	"water/internal/connectors/tokenapi"
	"water/internal/gate/permit"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

// Service and Account name the vault entry `water connect github` writes.
const (
	Service = "water.github"
	Account = "ceo"
)

const apiHost = "api.github.com"

const (
	defaultPerPage = 100
	maxPerPage     = 100
	// maxPages bounds how many pages Invoke will follow, independent of
	// per_page, so a misbehaving server (or an enormous repo) cannot loop
	// this forever.
	maxPages = 20
)

// ErrRateLimited is returned when GitHub reports the token's rate limit is
// exhausted (X-RateLimit-Remaining: 0), instead of looping on retries that
// cannot possibly succeed before the reported reset time.
type ErrRateLimited struct {
	Reset time.Time
}

func (e *ErrRateLimited) Error() string {
	if e.Reset.IsZero() {
		return "github: rate limit exhausted"
	}
	return fmt.Sprintf("github: rate limit exhausted, resets at %s", e.Reset.UTC().Format(time.RFC3339))
}

// PR is the compact shape Invoke returns for list_prs and Normalize
// consumes, matching fake.GitHubPR's fields.
type PR struct {
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

// Issue is the compact shape Invoke returns for list_issues, matching
// fake.GitHubIssue's fields.
type Issue struct {
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

// wirePR is the subset of GitHub's pull request resource this connector
// uses. GitHub's "list pulls" endpoint doesn't include full review state, so
// ReviewStatus/Reviewers are left empty here (mergeable_state and reviews
// need separate calls this slice doesn't make); the field stays in the
// output shape for fake-connector compatibility.
type wirePR struct {
	ID        int64      `json:"id"`
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	State     string     `json:"state"`
	Merged    bool       `json:"merged"`
	MergedAt  *string    `json:"merged_at"`
	User      wireUser   `json:"user"`
	HTMLURL   string     `json:"html_url"`
	UpdatedAt string     `json:"updated_at"`
	Assignees []wireUser `json:"assignees"`
}

type wireIssue struct {
	ID          int64       `json:"id"`
	Number      int         `json:"number"`
	Title       string      `json:"title"`
	State       string      `json:"state"`
	User        wireUser    `json:"user"`
	Assignee    *wireUser   `json:"assignee"`
	Labels      []wireLabel `json:"labels"`
	HTMLURL     string      `json:"html_url"`
	UpdatedAt   string      `json:"updated_at"`
	PullRequest *struct{}   `json:"pull_request"` // present iff this "issue" is actually a PR
}

type wireUser struct {
	Login string `json:"login"`
}

type wireLabel struct {
	Name string `json:"name"`
}

// GitHub is the connector. opts is nil in production and set in tests to
// point at an httptest server.
type GitHub struct {
	repo string
	opts *tokenapi.Options
}

// New builds the production connector for repo ("owner/name").
func New(repo string) *GitHub { return &GitHub{repo: repo} }

// NewWithOptions builds a connector against overridden tokenapi options, for
// tests.
func NewWithOptions(repo string, o *tokenapi.Options) *GitHub { return &GitHub{repo: repo, opts: o} }

func (*GitHub) Name() string { return "github" }

func (*GitHub) Credential() (string, string) { return Service, Account }

func (*GitHub) Functions() []connectors.Function {
	return []connectors.Function{
		{
			Name:        "list_prs",
			Description: "List pull requests on the configured repo (github.repo).",
			Level:       twins.R,
			Risk:        connectors.RiskLow,
			// A PR's title, author and review state come from GitHub
			// contributors, not the CEO.
			External: true,
			Schema: connectors.Schema{
				Properties: map[string]connectors.Property{
					"state": {Type: "string", Description: "filter: open, closed or merged"},
				},
			},
		},
		{
			Name:        "list_issues",
			Description: "List issues on the configured repo (github.repo).",
			Level:       twins.R,
			Risk:        connectors.RiskLow,
			External:    true,
			Schema: connectors.Schema{
				Properties: map[string]connectors.Property{
					"state":    {Type: "string", Description: "filter: open or closed"},
					"assignee": {Type: "string", Description: "filter by assignee login"},
				},
			},
		},
	}
}

// CheckStatus does a minimal, cheap, read-only live call (GET /rate_limit,
// which every authenticated token can call regardless of granted scopes and
// which never itself counts against the primary rate limit) to confirm a
// stored token actually authenticates. `water connect github --status` uses
// it.
func CheckStatus(ctx context.Context, s vault.Secret) error {
	cl, err := tokenapi.FromSecret(s, tokenapi.BearerAuth, apiHost, nil)
	if err != nil {
		return err
	}
	var out struct {
		Resources map[string]json.RawMessage `json:"resources"`
	}
	_, err = cl.GetJSON(ctx, "https://"+apiHost+"/rate_limit", nil, headers(), &out)
	return err
}

func (g *GitHub) Invoke(ctx context.Context, p permit.Permit) (json.RawMessage, error) {
	v, err := p.Open()
	if err != nil {
		return nil, err
	}
	if g.repo == "" {
		return nil, fmt.Errorf("github: no repo configured; set github.repo (see docs/real-connectors-setup.md)")
	}
	cl, err := tokenapi.FromSecret(v.Credential, tokenapi.BearerAuth, apiHost, g.opts)
	if err != nil {
		return nil, fmt.Errorf("github: %w; run `water connect github --token <PAT>`", err)
	}
	switch v.Function {
	case "list_prs":
		return g.listPRs(ctx, cl, v)
	case "list_issues":
		return g.listIssues(ctx, cl, v)
	}
	return nil, fmt.Errorf("github: unknown function %q", v.Function)
}

func headers() map[string]string {
	return map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
	}
}

// checkRateLimit surfaces a clear error the moment GitHub reports the token
// is out of requests, instead of retrying (or paginating further) into more
// 403s that cannot possibly succeed before Reset.
func checkRateLimit(status int, hdr http.Header) error {
	if hdr == nil {
		return nil
	}
	remaining := hdr.Get("X-RateLimit-Remaining")
	if remaining != "0" {
		return nil
	}
	if status != http.StatusForbidden && status != http.StatusTooManyRequests && status >= 200 && status < 300 {
		// A 2xx with Remaining: 0 just means this was the last request the
		// token had left; it succeeded, so don't fail it — the next page
		// request will hit this check again and stop before it dispatches.
		return nil
	}
	reset := time.Time{}
	if s := hdr.Get("X-RateLimit-Reset"); s != "" {
		if secs, err := strconv.ParseInt(s, 10, 64); err == nil {
			reset = time.Unix(secs, 0)
		}
	}
	return &ErrRateLimited{Reset: reset}
}

func (g *GitHub) listPRs(ctx context.Context, cl *tokenapi.Client, v permit.Call) (json.RawMessage, error) {
	state := strings.ToLower(tokenapi.ArgString(v.Args, "state"))
	if state == "" {
		state = "all"
	} else if state != "open" && state != "closed" && state != "merged" && state != "all" {
		return nil, fmt.Errorf("github: state must be open, closed or merged")
	}
	// GitHub's pulls endpoint only knows open/closed/all; "merged" is a
	// closed PR with merged_at set, so ask for "all" (or "closed") upstream
	// and filter client-side when the caller wants exactly merged/closed.
	upstreamState := state
	if state == "merged" {
		upstreamState = "closed"
	}

	endpoint := fmt.Sprintf("https://%s/repos/%s/pulls", apiHost, g.repo)
	var out []PR
	pageURL := endpoint
	query := url.Values{"state": {upstreamState}, "per_page": {strconv.Itoa(defaultPerPage)}, "sort": {"updated"}, "direction": {"desc"}}
	for page := 0; page < maxPages && pageURL != ""; page++ {
		var wire []wirePR
		q := query
		if page > 0 {
			q = nil // pageURL already carries the full query from Link
		}
		hdr, err := cl.GetJSON(ctx, pageURL, q, headers(), &wire)
		if err != nil {
			if rl := rateLimitFromErr(err, hdr); rl != nil {
				return nil, rl
			}
			return nil, err
		}
		if err := checkRateLimit(http.StatusOK, hdr); err != nil {
			return nil, err
		}
		for _, w := range wire {
			pr := toPR(w)
			if state == "merged" && !w.Merged {
				continue
			}
			if state == "closed" && w.Merged {
				continue
			}
			out = append(out, pr)
		}
		pageURL = nextLink(hdr)
	}
	if out == nil {
		out = []PR{}
	}
	return json.Marshal(out)
}

func (g *GitHub) listIssues(ctx context.Context, cl *tokenapi.Client, v permit.Call) (json.RawMessage, error) {
	state := strings.ToLower(tokenapi.ArgString(v.Args, "state"))
	if state == "" {
		state = "all"
	} else if state != "open" && state != "closed" && state != "all" {
		return nil, fmt.Errorf("github: state must be open or closed")
	}
	assignee := strings.ToLower(tokenapi.ArgString(v.Args, "assignee"))

	endpoint := fmt.Sprintf("https://%s/repos/%s/issues", apiHost, g.repo)
	var out []Issue
	pageURL := endpoint
	query := url.Values{"state": {state}, "per_page": {strconv.Itoa(defaultPerPage)}, "sort": {"updated"}, "direction": {"desc"}}
	if assignee != "" {
		query.Set("assignee", assignee)
	}
	for page := 0; page < maxPages && pageURL != ""; page++ {
		var wire []wireIssue
		q := query
		if page > 0 {
			q = nil
		}
		hdr, err := cl.GetJSON(ctx, pageURL, q, headers(), &wire)
		if err != nil {
			if rl := rateLimitFromErr(err, hdr); rl != nil {
				return nil, rl
			}
			return nil, err
		}
		if err := checkRateLimit(http.StatusOK, hdr); err != nil {
			return nil, err
		}
		for _, w := range wire {
			if w.PullRequest != nil {
				// GitHub's issues endpoint also returns PRs; exclude them so
				// list_issues and list_prs don't double-count the same item.
				continue
			}
			out = append(out, toIssue(w))
		}
		pageURL = nextLink(hdr)
	}
	if out == nil {
		out = []Issue{}
	}
	return json.Marshal(out)
}

// rateLimitFromErr converts a 403/429 APIError carrying Remaining: 0 headers
// into the clear ErrRateLimited sentinel.
func rateLimitFromErr(err error, hdr http.Header) error {
	status := tokenapi.Status(err)
	if status == 0 {
		return nil
	}
	return checkRateLimit(status, hdr)
}

// nextLink parses GitHub's RFC 5988 Link header and returns the "next" page
// URL, or "" when there is none (the last page).
func nextLink(hdr http.Header) string {
	if hdr == nil {
		return ""
	}
	for _, part := range strings.Split(hdr.Get("Link"), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		segs := strings.Split(part, ";")
		if len(segs) < 2 {
			continue
		}
		urlPart := strings.TrimSpace(segs[0])
		if !strings.HasPrefix(urlPart, "<") || !strings.HasSuffix(urlPart, ">") {
			continue
		}
		isNext := false
		for _, attr := range segs[1:] {
			attr = strings.TrimSpace(attr)
			if attr == `rel="next"` {
				isNext = true
				break
			}
		}
		if isNext {
			return strings.TrimSuffix(strings.TrimPrefix(urlPart, "<"), ">")
		}
	}
	return ""
}

func toPR(w wirePR) PR {
	state := w.State
	if w.Merged {
		state = "merged"
	}
	updated, _ := time.Parse(time.RFC3339, w.UpdatedAt)
	return PR{
		ID:        strconv.FormatInt(w.ID, 10),
		Number:    w.Number,
		Title:     w.Title,
		Author:    w.User.Login,
		State:     state,
		URL:       w.HTMLURL,
		UpdatedAt: updated.UTC().Format(time.RFC3339),
	}
}

func toIssue(w wireIssue) Issue {
	assignee := ""
	if w.Assignee != nil {
		assignee = w.Assignee.Login
	}
	labels := make([]string, 0, len(w.Labels))
	for _, l := range w.Labels {
		labels = append(labels, l.Name)
	}
	updated, _ := time.Parse(time.RFC3339, w.UpdatedAt)
	return Issue{
		ID:        strconv.FormatInt(w.ID, 10),
		Number:    w.Number,
		Title:     w.Title,
		Author:    w.User.Login,
		State:     w.State,
		Assignee:  assignee,
		Labels:    labels,
		URL:       w.HTMLURL,
		UpdatedAt: updated.UTC().Format(time.RFC3339),
	}
}

// Normalize maps PRs onto store.Commit and issues onto store.Issue, exactly
// as internal/connectors/fake.GitHub.Normalize does, so the two are
// interchangeable to every downstream reader.
func (g *GitHub) Normalize(fn string, raw json.RawMessage) ([]store.Record, error) {
	switch fn {
	case "list_prs":
		var prs []PR
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
				Repo:        g.repo,
				SHA:         fmt.Sprintf("pr-%d", pr.Number),
				Author:      pr.Author,
				Message:     msg,
				CommittedAt: at.UTC(),
				URL:         pr.URL,
			})
		}
		return out, nil
	case "list_issues":
		var issues []Issue
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
				Project:  g.repo,
				Priority: strings.Join(is.Labels, ","),
				URL:      is.URL,
			})
		}
		return out, nil
	}
	return nil, nil
}
