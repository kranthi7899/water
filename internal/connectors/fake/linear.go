package fake

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"water/internal/connectors"
	"water/internal/gate/permit"
	"water/internal/store"
	"water/internal/twins"
)

// ---- linear ----
//
// A demo-only, in-memory stand-in for a Linear connector. Same posture as
// github.go: no network call, no OAuth, no API token.

// LinearIssue is one fake Linear ticket.
type LinearIssue struct {
	ID         string `json:"id"`
	Identifier string `json:"identifier"` // e.g. "ENG-142"
	Title      string `json:"title"`
	Status     string `json:"status"`   // Backlog | Todo | In Progress | In Review | Done | Canceled
	Priority   string `json:"priority"` // Urgent | High | Medium | Low | No priority
	Assignee   string `json:"assignee,omitempty"`
	Project    string `json:"project"`
	URL        string `json:"url"`
}

// DefaultLinearIssues is the canonical demo seed: realistic, clearly-fictional
// tickets spread across a small startup's projects.
func DefaultLinearIssues() []LinearIssue {
	return []LinearIssue{
		{ID: "lin-eng142", Identifier: "ENG-142", Title: "Cart totals off by $0.01 on multi-currency orders", Status: "In Progress", Priority: "Urgent", Assignee: "priya-k", Project: "Checkout Revamp", URL: "https://linear.app/nimbus/issue/ENG-142"},
		{ID: "lin-eng118", Identifier: "ENG-118", Title: "Add SSO support for enterprise tier", Status: "In Review", Priority: "High", Assignee: "jsong", Project: "Platform Reliability", URL: "https://linear.app/nimbus/issue/ENG-118"},
		{ID: "lin-grw54", Identifier: "GRW-54", Title: "Redesign trial-to-paid email sequence", Status: "Todo", Priority: "Medium", Assignee: "rachel-l", Project: "Growth Experiments", URL: "https://linear.app/nimbus/issue/GRW-54"},
		{ID: "lin-onb9", Identifier: "ONB-9", Title: "First-run tooltip tour skips step 3 on Safari", Status: "Backlog", Priority: "Low", Project: "Onboarding Redesign", URL: "https://linear.app/nimbus/issue/ONB-9"},
	}
}

// Linear is the fake connector.
type Linear struct {
	mu     sync.Mutex
	issues []LinearIssue
}

// NewLinear builds the connector from explicit seed data.
func NewLinear(seed ...LinearIssue) *Linear { return &Linear{issues: seed} }

func (*Linear) Name() string                 { return "linear" }
func (*Linear) Credential() (string, string) { return "", "" }

func (*Linear) Functions() []connectors.Function {
	return []connectors.Function{
		{Name: "list_issues", Description: "List Linear tickets, optionally filtered.", Level: twins.R, Risk: connectors.RiskLow, External: true,
			Schema: connectors.Schema{Properties: map[string]connectors.Property{
				"query":   str("keyword filter over title and project"),
				"status":  str("filter by status"),
				"project": str("filter by project name"),
			}}},
	}
}

func (l *Linear) Invoke(_ context.Context, p permit.Permit) (json.RawMessage, error) {
	call, err := p.Open()
	if err != nil {
		return nil, err
	}
	if call.Function != "list_issues" {
		return nil, fmt.Errorf("linear: unknown function %q", call.Function)
	}
	query := strings.ToLower(strings.TrimSpace(arg(call.Args, "query")))
	status := strings.ToLower(arg(call.Args, "status"))
	project := strings.ToLower(arg(call.Args, "project"))
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []LinearIssue
	for _, is := range l.issues {
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
	return json.Marshal(out)
}

// matchesAny reports whether any whitespace-separated word of query appears
// as a substring of any haystack.
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

func (l *Linear) Normalize(fn string, raw json.RawMessage) ([]store.Record, error) {
	if fn != "list_issues" {
		return nil, nil
	}
	var issues []LinearIssue
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
