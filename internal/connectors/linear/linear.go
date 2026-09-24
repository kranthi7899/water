// Package linear is a real, read-only Linear connector: it lists issues
// through Linear's GraphQL API (api.linear.app/graphql), authenticated with
// a personal API key generated in Linear's own settings (no OAuth app
// needed). Its function name and Normalize output shape match
// internal/connectors/fake.Linear exactly, so anything already built against
// the demo fake works unchanged against this real connector once a key is
// configured.
//
// Linear's API is GraphQL: this package hand-rolls a single POST of
// {query, variables} to one endpoint, no client library, exactly as the
// slice that built it was asked to.
package linear

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"water/internal/connectors"
	"water/internal/connectors/tokenapi"
	"water/internal/gate/permit"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

// Service and Account name the vault entry `water connect linear` writes.
const (
	Service = "water.linear"
	Account = "ceo"
)

// apiHost is Linear's single GraphQL endpoint host. Unlike GitHub/HubSpot's
// Bearer scheme, Linear's own docs specify the raw API key with no scheme
// prefix as the Authorization header's value.
const apiHost = "api.linear.app"

const endpoint = "https://" + apiHost + "/graphql"

// pageSize bounds one GraphQL page; maxPages bounds how many pages Invoke
// will follow so a very large workspace cannot loop this forever.
const (
	pageSize = 100
	maxPages = 20
)

// Issue is the compact shape Invoke returns and Normalize consumes,
// matching fake.LinearIssue's fields.
type Issue struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"` // e.g. "ENG-142"
	Title      string `json:"title"`
	Status     string `json:"status"`   // Linear's workflow state name
	Priority   string `json:"priority"` // Urgent | High | Medium | Low | No priority
	Assignee   string `json:"assignee,omitempty"`
	Project    string `json:"project"`
	URL        string `json:"url"`
}

// wireIssue is one node of Linear's issues connection, trimmed to the
// fields this connector uses.
type wireIssue struct {
	ID         string  `json:"id"`
	Identifier string  `json:"identifier"`
	Title      string  `json:"title"`
	Priority   float64 `json:"priority"`
	State      *struct {
		Name string `json:"name"`
	} `json:"state"`
	Assignee *struct {
		Name string `json:"name"`
	} `json:"assignee"`
	Project *struct {
		Name string `json:"name"`
	} `json:"project"`
	URL string `json:"url"`
}

type pageInfo struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type issuesQueryResponse struct {
	Data struct {
		Issues struct {
			Nodes    []wireIssue `json:"nodes"`
			PageInfo pageInfo    `json:"pageInfo"`
		} `json:"issues"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

// issuesQuery lists issues, newest-updated first, one page at a time.
// Linear represents priority as an integer 0-4 (0 = no priority, 1 =
// urgent ... 4 = low); priorityLabel below maps it to the same words the
// fake connector and Linear's own UI use.
const issuesQuery = `
query Issues($first: Int!, $after: String) {
  issues(first: $first, after: $after, orderBy: updatedAt) {
    nodes {
      id
      identifier
      title
      priority
      state { name }
      assignee { name }
      project { name }
      url
    }
    pageInfo { hasNextPage endCursor }
  }
}`

func priorityLabel(p float64) string {
	switch int(p) {
	case 1:
		return "Urgent"
	case 2:
		return "High"
	case 3:
		return "Medium"
	case 4:
		return "Low"
	default:
		return "No priority"
	}
}

// Linear is the connector. opts is nil in production and set in tests to
// point at an httptest server.
type Linear struct {
	opts *tokenapi.Options
}

// New builds the production connector.
func New() *Linear { return &Linear{} }

// NewWithOptions builds a connector against overridden tokenapi options, for
// tests.
func NewWithOptions(o *tokenapi.Options) *Linear { return &Linear{opts: o} }

func (*Linear) Name() string { return "linear" }

func (*Linear) Credential() (string, string) { return Service, Account }

func (*Linear) Functions() []connectors.Function {
	return []connectors.Function{
		{
			Name:        "list_issues",
			Description: "List Linear tickets, optionally filtered.",
			Level:       twins.R,
			Risk:        connectors.RiskLow,
			// A ticket's title, assignee and project come from the team, not
			// the CEO directly.
			External: true,
			Schema: connectors.Schema{
				Properties: map[string]connectors.Property{
					"query":   {Type: "string", Description: "keyword filter over title and project"},
					"status":  {Type: "string", Description: "filter by status"},
					"project": {Type: "string", Description: "filter by project name"},
				},
			},
		},
	}
}

func (l *Linear) client(cred vault.Secret) (*tokenapi.Client, error) {
	return tokenapi.FromSecret(cred, tokenapi.RawAuth, apiHost, l.opts)
}

func (l *Linear) Invoke(ctx context.Context, p permit.Permit) (json.RawMessage, error) {
	v, err := p.Open()
	if err != nil {
		return nil, err
	}
	if v.Function != "list_issues" {
		return nil, fmt.Errorf("linear: unknown function %q", v.Function)
	}
	cl, err := l.client(v.Credential)
	if err != nil {
		return nil, fmt.Errorf("linear: %w; run `water connect linear --token <KEY>`", err)
	}

	query := strings.ToLower(tokenapi.ArgString(v.Args, "query"))
	status := strings.ToLower(tokenapi.ArgString(v.Args, "status"))
	project := strings.ToLower(tokenapi.ArgString(v.Args, "project"))

	var out []Issue
	after := ""
	for page := 0; page < maxPages; page++ {
		var resp issuesQueryResponse
		variables := map[string]any{"first": pageSize}
		if after != "" {
			variables["after"] = after
		}
		if _, err := cl.PostJSON(ctx, endpoint, map[string]string{"Content-Type": "application/json"}, map[string]any{
			"query":     issuesQuery,
			"variables": variables,
		}, &resp); err != nil {
			return nil, fmt.Errorf("linear: %w", err)
		}
		if len(resp.Errors) > 0 {
			return nil, fmt.Errorf("linear: %s", resp.Errors[0].Message)
		}
		for _, w := range resp.Data.Issues.Nodes {
			is := toIssue(w)
			if status != "" && strings.ToLower(is.Status) != status {
				continue
			}
			if project != "" && strings.ToLower(is.Project) != project {
				continue
			}
			if query != "" && !matchesAny(query, strings.ToLower(is.Title), strings.ToLower(is.Project), strings.ToLower(is.Identifier)) {
				continue
			}
			out = append(out, is)
		}
		if !resp.Data.Issues.PageInfo.HasNextPage {
			break
		}
		after = resp.Data.Issues.PageInfo.EndCursor
		if after == "" {
			break
		}
	}
	if out == nil {
		out = []Issue{}
	}
	return json.Marshal(out)
}

// matchesAny reports whether any whitespace-separated word of query appears
// as a substring of any haystack (the same word-substring rule the fake
// connector uses).
func matchesAny(query string, haystacks ...string) bool {
	for _, w := range strings.Fields(query) {
		for _, h := range haystacks {
			if strings.Contains(h, w) {
				return true
			}
		}
	}
	return false
}

func toIssue(w wireIssue) Issue {
	status, assignee, project := "", "", ""
	if w.State != nil {
		status = w.State.Name
	}
	if w.Assignee != nil {
		assignee = w.Assignee.Name
	}
	if w.Project != nil {
		project = w.Project.Name
	}
	return Issue{
		ID:         w.ID,
		Identifier: w.Identifier,
		Title:      w.Title,
		Status:     status,
		Priority:   priorityLabel(w.Priority),
		Assignee:   assignee,
		Project:    project,
		URL:        w.URL,
	}
}

// Normalize maps issues onto store.Issue, exactly as
// internal/connectors/fake.Linear.Normalize does.
func (l *Linear) Normalize(fn string, raw json.RawMessage) ([]store.Record, error) {
	if fn != "list_issues" {
		return nil, nil
	}
	var issues []Issue
	if err := json.Unmarshal(raw, &issues); err != nil {
		return nil, err
	}
	var out []store.Record
	for _, is := range issues {
		out = append(out, &store.Issue{
			Meta:     store.Meta{Source: l.Name(), SourceID: is.ID, External: true},
			Title:    fmt.Sprintf("%s %s", is.Identifier, is.Title),
			State:    is.Status,
			Assignee: is.Assignee,
			Project:  is.Project,
			Priority: is.Priority,
			URL:      is.URL,
		})
	}
	return out, nil
}

// CheckStatus does a minimal, cheap GraphQL query (the authenticated
// viewer's id) to confirm a stored key actually authenticates. `water
// connect linear --status` uses it.
func CheckStatus(ctx context.Context, s vault.Secret) error {
	return checkStatusWithOptions(ctx, s, nil)
}

// checkStatusWithOptions is CheckStatus parameterized over tokenapi options,
// so a test can point it at an httptest server.
func checkStatusWithOptions(ctx context.Context, s vault.Secret, o *tokenapi.Options) error {
	cl, err := tokenapi.FromSecret(s, tokenapi.RawAuth, apiHost, o)
	if err != nil {
		return err
	}
	var resp struct {
		Data struct {
			Viewer struct {
				ID string `json:"id"`
			} `json:"viewer"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if _, err := cl.PostJSON(ctx, endpoint, map[string]string{"Content-Type": "application/json"}, map[string]any{
		"query": `query { viewer { id } }`,
	}, &resp); err != nil {
		return err
	}
	if len(resp.Errors) > 0 {
		return fmt.Errorf("linear: %s", resp.Errors[0].Message)
	}
	if resp.Data.Viewer.ID == "" {
		return fmt.Errorf("linear: unexpected empty viewer response")
	}
	return nil
}
