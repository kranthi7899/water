// This file adds Slice V's/D6's next two deterministic writers (Phase 1a,
// docs/slices/UI.md): in_workspace (store.LinkInWorkspace), evaluated
// against internal/workspaces' loaded rules, and the first for_project
// (store.LinkForProject) writer, which reuses internal/roster's existing
// project -> member_of -> team relationship rather than inventing a new
// one. Both follow this package's own conventions (recordlinks.go's doc
// comment): code and stored records only, never a model's guess, and
// every write goes through store.AddLink, so re-running a writer over an
// unchanged record never duplicates an edge.
package recordlinks

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"water/internal/roster"
	"water/internal/store"
	"water/internal/workspaces"
)

// Node types this file's writers use, alongside PersonType and
// DecisionType above.
const (
	IssueType     = "issue"
	ClientType    = "client"
	ProjectType   = "project"
	WorkspaceType = "workspace"
)

// identifierRe matches a Linear identifier's team-key prefix at the start
// of an issue title. store.Issue carries no separate identifier column —
// the Linear connector's Normalize (internal/connectors/linear/linear.go)
// always prefixes Title with "IDENTIFIER Title text" (e.g. "WAT-123 Fix
// the thing") — so this is the only place the prefix survives once an
// issue is a store.Issue.
var identifierRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*)-\d+\b`)

// IdentifierTeamKey returns the Linear team key from an issue's Title
// ("WAT-123 Fix the thing" -> "WAT"), or "" if title doesn't start with a
// recognizable identifier. It is never guessed at: a title with no
// identifier prefix (or a malformed one) simply yields no key.
func IdentifierTeamKey(title string) string {
	m := identifierRe.FindStringSubmatch(title)
	if m == nil {
		return ""
	}
	return strings.ToUpper(m[1])
}

// ForProjectFromIssue links a Linear issue to every roster project on the
// team whose linear_key matches the issue's identifier prefix
// ("WAT-123" -> "WAT" -> team "water" -> its projects, via
// roster.TeamByLinearKey and roster.ProjectsForTeam). An unrecognized
// prefix, or a team with no projects, writes nothing.
func ForProjectFromIssue(ctx context.Context, st *store.Store, issue store.Issue) error {
	if issue.SourceID == "" {
		return nil
	}
	key := IdentifierTeamKey(issue.Title)
	if key == "" {
		return nil
	}
	team, err := roster.TeamByLinearKey(ctx, st, key)
	if errors.Is(err, roster.ErrNoSuchTeam) {
		return nil
	}
	if err != nil {
		return err
	}
	projects, err := roster.ProjectsForTeam(ctx, st, team.SourceID)
	if err != nil {
		return err
	}
	for _, p := range projects {
		if err := st.AddLink(ctx, store.Link{Kind: store.LinkForProject, FromType: IssueType, FromID: issue.SourceID, ToType: ProjectType, ToID: p}); err != nil {
			return err
		}
	}
	return nil
}

// ForProjectFromClient links a roster client to every project on the team
// its own clients.product names (twins/ceo/seed/people.yaml's convention:
// product holds a team id, e.g. "yt-recamendo" — see
// internal/roster/roster.go's yamlClient and store.Client.Product). An
// empty or unrecognized product writes nothing.
func ForProjectFromClient(ctx context.Context, st *store.Store, client store.Client) error {
	if client.SourceID == "" || client.Product == "" {
		return nil
	}
	projects, err := roster.ProjectsForTeam(ctx, st, client.Product)
	if err != nil {
		return err
	}
	for _, p := range projects {
		if err := st.AddLink(ctx, store.Link{Kind: store.LinkForProject, FromType: ClientType, FromID: client.SourceID, ToType: ProjectType, ToID: p}); err != nil {
			return err
		}
	}
	return nil
}

// InWorkspace writes fromType/fromID -> in_workspace -> workspace(id) for
// every workspace in registry whose membership rules match c
// (store.LinkInWorkspace, docs/slices/UI.md Phase 1a). A record matching
// several workspaces gets an edge to each; a record matching none writes
// nothing. store.AddLink is idempotent, so re-running this over an
// unchanged record never duplicates an edge. A nil registry (a twin with
// no workspaces of its own) is a deliberate no-op.
func InWorkspace(ctx context.Context, st *store.Store, registry *workspaces.Registry, fromType, fromID string, c workspaces.Candidate) error {
	if registry == nil || fromType == "" || fromID == "" {
		return nil
	}
	for _, spec := range registry.MatchingSpecs(c) {
		if err := st.AddLink(ctx, store.Link{Kind: store.LinkInWorkspace, FromType: fromType, FromID: fromID, ToType: WorkspaceType, ToID: spec.ID}); err != nil {
			return err
		}
	}
	return nil
}

// InWorkspaceForIssue is InWorkspace specialized for a Linear issue,
// matched only on its identifier's team key.
func InWorkspaceForIssue(ctx context.Context, st *store.Store, registry *workspaces.Registry, issue store.Issue) error {
	return InWorkspace(ctx, st, registry, IssueType, issue.SourceID, workspaces.Candidate{TeamKey: IdentifierTeamKey(issue.Title)})
}

// InWorkspaceForClient is InWorkspace specialized for a roster client,
// matched only on its own id (a `clients: all` or `clients: [...]` rule).
func InWorkspaceForClient(ctx context.Context, st *store.Store, registry *workspaces.Registry, client store.Client) error {
	return InWorkspace(ctx, st, registry, ClientType, client.SourceID, workspaces.Candidate{ClientID: client.SourceID})
}
