package roster

import (
	"context"
	"fmt"
	"strings"

	"water/internal/store"
)

// ErrNoSuchIdentity means no roster person has key set to value — a
// resolution miss (e.g. a Linear Owner label with no matching person),
// never a system error.
var ErrNoSuchIdentity = fmt.Errorf("roster: no person with that identity")

// ErrNoSuchTeam means no roster team has that Linear key — a resolution
// miss (an issue from a Linear team the roster doesn't know about yet),
// never a system error.
var ErrNoSuchTeam = fmt.Errorf("roster: no team with that linear key")

// PersonByIdentity resolves a connector's own reference to a person — e.g.
// key "linear_owner_label", value "Theo" — to the roster's Person record.
// The roster is small (people, not messages), so this scans every person
// rather than needing a dedicated index; a caller resolving many labels in
// one pass should list people once itself instead of calling this in a
// loop.
func PersonByIdentity(ctx context.Context, st *store.Store, key, value string) (*store.Person, error) {
	if value == "" {
		return nil, ErrNoSuchIdentity
	}
	people, err := store.List[store.Person](ctx, st, store.Query{Source: "seed"})
	if err != nil {
		return nil, fmt.Errorf("roster: %w", err)
	}
	for _, p := range people {
		if p.Identity(key) == value {
			return &p, nil
		}
	}
	return nil, ErrNoSuchIdentity
}

// TeamByLinearKey resolves a Linear team key (e.g. "WAT", matched
// case-insensitively since a Linear identifier prefix is always upper-case
// but this guards against a seed file typo) to the roster's Team record.
// internal/recordlinks' for_project writer is the caller: an issue's
// identifier prefix ("WAT-123" -> "WAT") resolves to a team here, then to
// that team's projects via ProjectsForTeam.
func TeamByLinearKey(ctx context.Context, st *store.Store, key string) (*store.Team, error) {
	if key == "" {
		return nil, ErrNoSuchTeam
	}
	teams, err := store.List[store.Team](ctx, st, store.Query{Source: "seed"})
	if err != nil {
		return nil, fmt.Errorf("roster: %w", err)
	}
	for _, t := range teams {
		if strings.EqualFold(t.LinearKey, key) {
			team := t
			return &team, nil
		}
	}
	return nil, ErrNoSuchTeam
}

// ProjectsForTeam returns the source_ids of every roster project on team
// teamID (Write's own project -> member_of -> team edge), sorted for a
// deterministic caller. A team with no projects, or an unknown teamID,
// returns an empty slice, not an error.
func ProjectsForTeam(ctx context.Context, st *store.Store, teamID string) ([]string, error) {
	if teamID == "" {
		return nil, nil
	}
	links, err := st.LinksTo(ctx, "team", teamID, store.LinkMemberOf)
	if err != nil {
		return nil, fmt.Errorf("roster: %w", err)
	}
	var out []string
	for _, l := range links {
		if l.FromType == "project" {
			out = append(out, l.FromID)
		}
	}
	return out, nil
}
