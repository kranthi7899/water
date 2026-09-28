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
// matching fake.LinearIssue's fields, plus docs/slices/UI.md Phase 4's
// read-extension fields (U4 sub-question: a read-only extension to an
// existing connector's query is not a new connector) that
// internal/dashboards/compute.go's Delivery dashboard needs: Team,
// StateType, PriorityRank, DueDate, Labels, Relations and
// InverseRelations. These are additive and optional — every existing
// caller that only reads ID/Identifier/Title/Status/Priority/Assignee/
// Project/URL is unaffected.
type Issue struct {
	ID           string   `json:"id"`
	Identifier   string   `json:"identifier"` // e.g. "ENG-142"
	Title        string   `json:"title"`
	Status       string   `json:"status"`                  // Linear's workflow state name
	StateType    string   `json:"state_type,omitempty"`    // Linear's own coarse state.type: triage|backlog|unstarted|started|completed|cancelled
	Priority     string   `json:"priority"`                // Urgent | High | Medium | Low | No priority
	PriorityRank int      `json:"priority_rank,omitempty"` // Linear's raw 0-4 priority (1 = Urgent)
	Assignee     string   `json:"assignee,omitempty"`
	Project      string   `json:"project"`
	Team         string   `json:"team,omitempty"`     // the owning team's key, e.g. "CRA"
	DueDate      string   `json:"due_date,omitempty"` // "YYYY-MM-DD", empty if unset
	Labels       []string `json:"labels,omitempty"`
	// Relations is this issue's own outbound relations (e.g. "this issue
	// blocks that one"); InverseRelations is relations where this issue is
	// the target ("that issue blocks this one"). Both are read-only,
	// fetched from Linear's own relations/inverseRelations connections —
	// no new write surface.
	Relations        []Relation `json:"relations,omitempty"`
	InverseRelations []Relation `json:"inverse_relations,omitempty"`
	URL              string     `json:"url"`
}

// Relation is one edge of an issue's relations or inverseRelations
// connection: Type is Linear's own relation type ("blocks", "related",
// "duplicate", ...), Identifier/StateType describe the *other* issue in
// the edge (the blocked/blocking issue), never this one.
type Relation struct {
	Type       string `json:"type"`
	Identifier string `json:"identifier"`
	StateType  string `json:"state_type"`
}

// wireIssue is one node of Linear's issues connection, trimmed to the
// fields this connector uses.
type wireIssue struct {
	ID         string  `json:"id"`
	Identifier string  `json:"identifier"`
	Title      string  `json:"title"`
	Priority   float64 `json:"priority"`
	DueDate    *string `json:"dueDate"`
	State      *struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"state"`
	Assignee *struct {
		Name string `json:"name"`
	} `json:"assignee"`
	Project *struct {
		Name string `json:"name"`
	} `json:"project"`
	Team *struct {
		Key string `json:"key"`
	} `json:"team"`
	Labels *struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	} `json:"labels"`
	Relations        *wireRelationConnection `json:"relations"`
	InverseRelations *wireRelationConnection `json:"inverseRelations"`
	URL              string                  `json:"url"`
}

// wireRelationConnection is the shape of both the "relations" and
// "inverseRelations" fields; only one of RelatedIssue (relations) or Issue
// (inverseRelations) is ever populated per node, matching which of the two
// connections it came from.
type wireRelationConnection struct {
	Nodes []struct {
		Type         string           `json:"type"`
		RelatedIssue *wireRelatedNode `json:"relatedIssue"`
		Issue        *wireRelatedNode `json:"issue"`
	} `json:"nodes"`
}

type wireRelatedNode struct {
	Identifier string `json:"identifier"`
	State      *struct {
		Type string `json:"type"`
	} `json:"state"`
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
      dueDate
      state { name type }
      assignee { name }
      project { name }
      team { key }
      labels { nodes { name } }
      relations { nodes { type relatedIssue { identifier state { type } } } }
      inverseRelations { nodes { type issue { identifier state { type } } } }
      url
    }
    pageInfo { hasNextPage endCursor }
  }
}`

// resolveIssueQuery resolves a human-readable identifier (e.g. "CRA-3") to
// the internal UUID create_comment/set_issue_priority's mutations need --
// Linear's `issue(id: ...)` query accepts either form, confirmed live
// against the owner's real workspace (2026-09-27, read-only), but the two
// write mutations below are only verified against Linear's documented
// schema to require the UUID, so this resolution step runs first for both.
const resolveIssueQuery = `
query ResolveIssue($id: String!) {
  issue(id: $id) { id identifier url }
}`

// createCommentMutation is docs/slices/UI.md U4's linear.create_comment.
const createCommentMutation = `
mutation CreateComment($issueId: String!, $body: String!) {
  commentCreate(input: { issueId: $issueId, body: $body }) {
    success
    comment { id url }
  }
}`

// setPriorityMutation is docs/slices/UI.md U4's linear.set_issue_priority.
const setPriorityMutation = `
mutation SetPriority($id: String!, $priority: Int!) {
  issueUpdate(id: $id, input: { priority: $priority }) {
    success
    issue { id identifier priority }
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

// priorityRank is priorityLabel's inverse, for set_issue_priority's input:
// a case-insensitive match against the exact five labels priorityLabel
// produces. ok is false for anything else, so a bad or misspelled label
// refuses cleanly rather than silently landing on "No priority".
func priorityRank(label string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "urgent":
		return 1, true
	case "high":
		return 2, true
	case "medium":
		return 3, true
	case "low":
		return 4, true
	case "no priority":
		return 0, true
	default:
		return 0, false
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
			Activity:    "Checking tickets in Linear",
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
		{
			Name:        "create_comment",
			Description: "Post a comment on a Linear issue. This has an external effect (visible to the team) and cannot be undone.",
			Activity:    "Commenting on a Linear issue",
			Level:       twins.A,
			Risk:        connectors.RiskMedium,
			External:    false,
			Schema: connectors.Schema{
				Properties: map[string]connectors.Property{
					"issue": {Type: "string", Description: "issue identifier, e.g. \"CRA-3\""},
					"body":  {Type: "string", Description: "comment text"},
					"simulated_relay": {Type: "boolean", Description: "mark this comment as a simulated stand-in for a reply " +
						"from someone with no real connector yet (e.g. a chat platform water doesn't integrate with). When true, " +
						"the connector itself enforces a fixed \"(simulated) Relayed:\" prefix on the posted body -- this cannot " +
						"be set or spoofed by the model's own wording, only by this flag."},
				},
				Required: []string{"issue", "body"},
			},
		},
		{
			Name:        "set_issue_priority",
			Description: "Change a Linear issue's priority. This has an external effect (visible to the team) and cannot be undone.",
			Activity:    "Updating a Linear issue's priority",
			Level:       twins.A,
			Risk:        connectors.RiskMedium,
			External:    false,
			Schema: connectors.Schema{
				Properties: map[string]connectors.Property{
					"issue":    {Type: "string", Description: "issue identifier, e.g. \"CRA-3\""},
					"priority": {Type: "string", Description: "one of: Urgent, High, Medium, Low, No priority"},
				},
				Required: []string{"issue", "priority"},
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
	cl, err := l.client(v.Credential)
	if err != nil {
		return nil, fmt.Errorf("linear: %w; run `water connect linear --token <KEY>`", err)
	}
	switch v.Function {
	case "list_issues":
		return l.listIssues(ctx, cl, v.Args)
	case "create_comment":
		return l.createComment(ctx, cl, v.Args)
	case "set_issue_priority":
		return l.setIssuePriority(ctx, cl, v.Args)
	default:
		return nil, fmt.Errorf("linear: unknown function %q", v.Function)
	}
}

// listIssues is list_issues' implementation, called directly by tests and
// by Invoke's dispatch above.
func (l *Linear) listIssues(ctx context.Context, cl *tokenapi.Client, args map[string]any) (json.RawMessage, error) {
	query := strings.ToLower(tokenapi.ArgString(args, "query"))
	status := strings.ToLower(tokenapi.ArgString(args, "status"))
	project := strings.ToLower(tokenapi.ArgString(args, "project"))

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

// relayedCommentPrefix must match internal/gateway/artifact.go's own
// unexported copy of the same string exactly: that package derives a note
// artifact's Simulated badge purely by checking whether a tool result's
// body starts with this text, so the two constants have to agree even
// though they live in different packages with no shared import between
// them. docs/slices/UI.md U4: "a relayed comment must start with a
// code-added '(simulated) Relayed:' prefix, enforced in the connector, not
// the prompt."
const relayedCommentPrefix = "(simulated) Relayed:"

// resolveIssueResponse is resolveIssueQuery's wire shape.
type resolveIssueResponse struct {
	Data struct {
		Issue *struct {
			ID         string `json:"id"`
			Identifier string `json:"identifier"`
			URL        string `json:"url"`
		} `json:"issue"`
	} `json:"data"`
	Errors []gqlError `json:"errors"`
}

type gqlError struct {
	Message string `json:"message"`
}

// resolveIssue resolves a human-readable identifier (e.g. "CRA-3") to its
// internal id/url, shared by createComment and setIssuePriority. A missing
// or unknown issue refuses with a clear error rather than a nil-pointer
// panic or a mutation call against an empty id.
func (l *Linear) resolveIssue(ctx context.Context, cl *tokenapi.Client, identifier string) (id, resolvedIdentifier, url string, err error) {
	if identifier == "" {
		return "", "", "", fmt.Errorf("linear: issue is required")
	}
	var resp resolveIssueResponse
	if _, err := cl.PostJSON(ctx, endpoint, map[string]string{"Content-Type": "application/json"}, map[string]any{
		"query":     resolveIssueQuery,
		"variables": map[string]any{"id": identifier},
	}, &resp); err != nil {
		return "", "", "", fmt.Errorf("linear: %w", err)
	}
	if len(resp.Errors) > 0 {
		return "", "", "", fmt.Errorf("linear: %s", resp.Errors[0].Message)
	}
	if resp.Data.Issue == nil {
		return "", "", "", fmt.Errorf("linear: no such issue %q", identifier)
	}
	return resp.Data.Issue.ID, resp.Data.Issue.Identifier, resp.Data.Issue.URL, nil
}

// createCommentOutput is create_comment's JSON output. Relay, Title, Body
// and Source match internal/gateway/artifact.go's noteArtifact contract
// exactly (docs/slices/UI.md Phase 6, U1-A): Relay is always true on a
// successful post (every comment this connector makes is worth surfacing
// as a note in the Activity HUD, same as a drafted email already is), and
// Simulated is derived downstream purely from whether Body starts with
// relayedCommentPrefix -- never trusted from a field this connector sets.
type createCommentOutput struct {
	ID     string `json:"id"`
	Issue  string `json:"issue"`
	URL    string `json:"url"`
	Body   string `json:"body"`
	Relay  bool   `json:"relay"`
	Title  string `json:"title"`
	Source string `json:"source"`
}

type createCommentResponse struct {
	Data struct {
		CommentCreate struct {
			Success bool `json:"success"`
			Comment struct {
				ID  string `json:"id"`
				URL string `json:"url"`
			} `json:"comment"`
		} `json:"commentCreate"`
	} `json:"data"`
	Errors []gqlError `json:"errors"`
}

// createComment is create_comment's implementation, called directly by
// tests and by Invoke's dispatch above.
func (l *Linear) createComment(ctx context.Context, cl *tokenapi.Client, args map[string]any) (json.RawMessage, error) {
	identifier := tokenapi.ArgString(args, "issue")
	body := strings.TrimSpace(tokenapi.ArgString(args, "body"))
	if body == "" {
		return nil, fmt.Errorf("linear: body is required")
	}
	simulatedRelay, _ := args["simulated_relay"].(bool)
	if simulatedRelay {
		// Strip any prefix-like text the caller (ultimately a model call)
		// supplied before prepending the canonical one fresh, so the
		// caller can never smuggle a different, near-miss or absent
		// prefix past the note artifact's Simulated check downstream.
		body = strings.TrimSpace(strings.TrimPrefix(body, relayedCommentPrefix))
		body = relayedCommentPrefix + " " + body
	}

	issueID, resolvedIdentifier, issueURL, err := l.resolveIssue(ctx, cl, identifier)
	if err != nil {
		return nil, err
	}

	var resp createCommentResponse
	if _, err := cl.PostJSON(ctx, endpoint, map[string]string{"Content-Type": "application/json"}, map[string]any{
		"query":     createCommentMutation,
		"variables": map[string]any{"issueId": issueID, "body": body},
	}, &resp); err != nil {
		return nil, fmt.Errorf("linear: %w", err)
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("linear: %s", resp.Errors[0].Message)
	}
	if !resp.Data.CommentCreate.Success {
		return nil, fmt.Errorf("linear: comment was not created")
	}

	return json.Marshal(createCommentOutput{
		ID:     resp.Data.CommentCreate.Comment.ID,
		Issue:  resolvedIdentifier,
		URL:    resp.Data.CommentCreate.Comment.URL,
		Body:   body,
		Relay:  true,
		Title:  fmt.Sprintf("Comment on %s", resolvedIdentifier),
		Source: issueURL,
	})
}

// setIssuePriorityOutput is set_issue_priority's JSON output.
type setIssuePriorityOutput struct {
	Issue    string `json:"issue"`
	Priority string `json:"priority"`
	URL      string `json:"url"`
}

type setPriorityResponse struct {
	Data struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	} `json:"data"`
	Errors []gqlError `json:"errors"`
}

// setIssuePriority is set_issue_priority's implementation, called directly
// by tests and by Invoke's dispatch above.
func (l *Linear) setIssuePriority(ctx context.Context, cl *tokenapi.Client, args map[string]any) (json.RawMessage, error) {
	identifier := tokenapi.ArgString(args, "issue")
	label := tokenapi.ArgString(args, "priority")
	rank, ok := priorityRank(label)
	if !ok {
		return nil, fmt.Errorf("linear: priority %q is not one of Urgent, High, Medium, Low, No priority", label)
	}

	issueID, resolvedIdentifier, issueURL, err := l.resolveIssue(ctx, cl, identifier)
	if err != nil {
		return nil, err
	}

	var resp setPriorityResponse
	if _, err := cl.PostJSON(ctx, endpoint, map[string]string{"Content-Type": "application/json"}, map[string]any{
		"query":     setPriorityMutation,
		"variables": map[string]any{"id": issueID, "priority": rank},
	}, &resp); err != nil {
		return nil, fmt.Errorf("linear: %w", err)
	}
	if len(resp.Errors) > 0 {
		return nil, fmt.Errorf("linear: %s", resp.Errors[0].Message)
	}
	if !resp.Data.IssueUpdate.Success {
		return nil, fmt.Errorf("linear: issue priority was not updated")
	}

	return json.Marshal(setIssuePriorityOutput{
		Issue:    resolvedIdentifier,
		Priority: priorityLabel(float64(rank)),
		URL:      issueURL,
	})
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
	status, stateType, assignee, project, team := "", "", "", "", ""
	if w.State != nil {
		status, stateType = w.State.Name, w.State.Type
	}
	if w.Assignee != nil {
		assignee = w.Assignee.Name
	}
	if w.Project != nil {
		project = w.Project.Name
	}
	if w.Team != nil {
		team = w.Team.Key
	}
	dueDate := ""
	if w.DueDate != nil {
		dueDate = *w.DueDate
	}
	var labels []string
	if w.Labels != nil {
		for _, n := range w.Labels.Nodes {
			labels = append(labels, n.Name)
		}
	}
	return Issue{
		ID:               w.ID,
		Identifier:       w.Identifier,
		Title:            w.Title,
		Status:           status,
		StateType:        stateType,
		Priority:         priorityLabel(w.Priority),
		PriorityRank:     int(w.Priority),
		Assignee:         assignee,
		Project:          project,
		Team:             team,
		DueDate:          dueDate,
		Labels:           labels,
		Relations:        toRelations(w.Relations, false),
		InverseRelations: toRelations(w.InverseRelations, true),
		URL:              w.URL,
	}
}

// toRelations flattens a wireRelationConnection into Relations, reading
// RelatedIssue (the "relations" connection) or Issue (the "inverseRelations"
// connection) per inverse, and skipping a node whose other-side issue is
// missing (Linear can return a null reference for a deleted issue).
func toRelations(c *wireRelationConnection, inverse bool) []Relation {
	if c == nil {
		return nil
	}
	var out []Relation
	for _, n := range c.Nodes {
		other := n.RelatedIssue
		if inverse {
			other = n.Issue
		}
		if other == nil {
			continue
		}
		stateType := ""
		if other.State != nil {
			stateType = other.State.Type
		}
		out = append(out, Relation{Type: n.Type, Identifier: other.Identifier, StateType: stateType})
	}
	return out
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
