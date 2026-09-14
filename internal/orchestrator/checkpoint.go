package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Checkpointer persists State between steps so a run can be resumed.
type Checkpointer interface {
	Name() string
	Save(ctx context.Context, s *State) error
	Load(ctx context.Context, runID string) (*State, error)
}

// NoopCheckpointer discards checkpoints (tests; Phase 1 default).
type NoopCheckpointer struct{}

func (NoopCheckpointer) Name() string                       { return "noop" }
func (NoopCheckpointer) Save(context.Context, *State) error { return nil }
func (NoopCheckpointer) Load(context.Context, string) (*State, error) {
	return nil, ErrNoCheckpoint
}

// FileCheckpointer is the durable implementation (Part 3C): one JSON snapshot
// per run at <Dir>/<run-id>.json, written atomically after every superstep.
// The run-id is the caller's to persist — `water orchestrate --resume <id>`
// passes it back in; Load never invents one.
type FileCheckpointer struct {
	Dir           string
	Orchestrators []string // roles permitted to write FinalOutput after restore
}

// FileCheckpointerName is the config value selecting this implementation.
const FileCheckpointerName = "file"

func (f *FileCheckpointer) Name() string { return FileCheckpointerName }

// Path returns the checkpoint file for a run.
func (f *FileCheckpointer) Path(runID string) string {
	return filepath.Join(f.Dir, filepath.Base(runID)+".json")
}

func (f *FileCheckpointer) Save(_ context.Context, s *State) error {
	if f.Dir == "" {
		return errors.New("file checkpointer: Dir is required")
	}
	if err := os.MkdirAll(f.Dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.Snapshot(), "", "  ")
	if err != nil {
		return err
	}
	p := f.Path(s.RunID)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (f *FileCheckpointer) Load(_ context.Context, runID string) (*State, error) {
	b, err := os.ReadFile(f.Path(runID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNoCheckpoint, runID)
		}
		return nil, err
	}
	var snap Snapshot
	if err := json.Unmarshal(b, &snap); err != nil {
		return nil, fmt.Errorf("checkpoint %s is corrupt: %w", runID, err)
	}
	if snap.RunID != runID {
		return nil, fmt.Errorf("checkpoint %s carries run id %q", runID, snap.RunID)
	}
	return Restore(snap, f.Orchestrators), nil
}

// CheckpointInfo summarises a stored checkpoint for `water orchestrate --list`.
type CheckpointInfo struct {
	RunID     string    `json:"run_id"`
	Brief     string    `json:"brief"`
	StepCount int       `json:"step_count"`
	Complete  bool      `json:"complete"`
	SavedAt   time.Time `json:"saved_at"`
}

// List returns stored checkpoints, newest first.
func (f *FileCheckpointer) List() ([]CheckpointInfo, error) {
	entries, err := os.ReadDir(f.Dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []CheckpointInfo
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(f.Dir, e.Name()))
		if err != nil {
			continue
		}
		var snap Snapshot
		if json.Unmarshal(b, &snap) != nil {
			continue
		}
		out = append(out, CheckpointInfo{RunID: snap.RunID, Brief: snap.Brief, StepCount: snap.StepCount, Complete: snap.FinalOutput != nil, SavedAt: snap.SavedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SavedAt.After(out[j].SavedAt) })
	return out, nil
}
