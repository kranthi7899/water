package orchestrator

import "context"

// Checkpointer persists State between steps so a run can be resumed.
// Phase 1 ships NoopCheckpointer; `water orchestrate --resume` becomes a
// provider swap, not a rewrite.
type Checkpointer interface {
	Name() string
	Save(ctx context.Context, s *State) error
	Load(ctx context.Context, runID string) (*State, error)
}

// NoopCheckpointer discards checkpoints.
type NoopCheckpointer struct{}

func (NoopCheckpointer) Name() string                       { return "noop" }
func (NoopCheckpointer) Save(context.Context, *State) error { return nil }
func (NoopCheckpointer) Load(context.Context, string) (*State, error) {
	return nil, ErrNoCheckpoint
}
