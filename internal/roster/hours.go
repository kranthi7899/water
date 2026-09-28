package roster

import (
	"context"
	"fmt"
	"time"

	"water/internal/store"
)

const hoursPerWeek = 40

// HoursInvested derives hours a person has put into a project from an
// allocation fraction (of a 40h week) and the project's start date, as of
// at — never stored, always computed fresh, so it can never go stale.
// Before the project's own start (or a non-positive fraction), 0.
func HoursInvested(fraction float64, start, at time.Time) float64 {
	if fraction <= 0 || start.IsZero() || at.Before(start) {
		return 0
	}
	weeks := at.Sub(start).Hours() / (24 * 7)
	return fraction * hoursPerWeek * weeks
}

// PersonProjectHours resolves personID's allocated_to fraction for
// projectID and the project's own start date from st, returning the
// derived hours invested as of at. 0, nil if the person has no
// allocated_to link to that project (not allocated is not an error).
func PersonProjectHours(ctx context.Context, st *store.Store, personID, projectID string, at time.Time) (float64, error) {
	links, err := st.LinksFrom(ctx, "person", personID, store.LinkAllocated)
	if err != nil {
		return 0, fmt.Errorf("roster: %w", err)
	}
	var fraction float64
	var allocated bool
	for _, l := range links {
		if l.ToID == projectID {
			fraction, allocated = l.Fraction, true
			break
		}
	}
	if !allocated {
		return 0, nil
	}
	proj, err := store.Get[store.Project](ctx, st, "seed", projectID)
	if err != nil {
		return 0, fmt.Errorf("roster: project %q: %w", projectID, err)
	}
	return HoursInvested(fraction, proj.StartAt, at), nil
}
