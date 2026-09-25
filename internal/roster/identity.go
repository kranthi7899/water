package roster

import (
	"context"
	"fmt"

	"water/internal/store"
)

// ErrNoSuchIdentity means no roster person has key set to value — a
// resolution miss (e.g. a Linear Owner label with no matching person),
// never a system error.
var ErrNoSuchIdentity = fmt.Errorf("roster: no person with that identity")

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
