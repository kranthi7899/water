package sidecar

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Reasons Check returns when a record does not clear the gate. Empty string
// means the record passes. These match the GET /v1/router reason strings
// documented in docs/functiongemma.md ("eval_missing", "eval_stale",
// "eval_failed") minus eval_missing, which is the caller's concern (the file
// not existing at all, before there is any record to check).
const (
	ReasonEvalStale  = "eval_stale"
	ReasonEvalFailed = "eval_failed"
)

// Gate thresholds a Tier 1 live eval must clear before Tier 1 is allowed to
// answer real turns. See docs/functiongemma.md and docs/slices/R.md §10.
const (
	MaxFARate        = 0.01
	MaxWilson95Upper = 0.02
	MinN             = 200
	MaxWarmP95Ms     = 400
)

// EvalRecord is the on-disk record written by `water route eval --tier1`
// (task R-18) to $WATER_HOME/router/tier1_eval.json.
type EvalRecord struct {
	ModelSHA256   string    `json:"model_sha256"`
	RegistryHash  string    `json:"registry_hash"`
	N             int       `json:"n"`
	FalseAccepts  int       `json:"false_accepts"`
	FARate        float64   `json:"fa_rate"`
	Wilson95Upper float64   `json:"wilson95_upper"`
	WarmP95Ms     int       `json:"warm_p95_ms"`
	At            time.Time `json:"at"`
}

// Check is a pure function: it reports whether rec clears every Tier 1 gate
// threshold and matches the currently-expected model/registry identity. It
// makes no I/O and has no dependency on wall-clock time.
//
// reason is "" when ok is true, ReasonEvalStale when the record was produced
// against a different model or intent registry, and ReasonEvalFailed when
// the record's own measurements miss a threshold.
func (rec EvalRecord) Check(expectedModelSHA256, expectedRegistryHash string) (ok bool, reason string) {
	if rec.ModelSHA256 != expectedModelSHA256 || rec.RegistryHash != expectedRegistryHash {
		return false, ReasonEvalStale
	}
	if rec.FARate > MaxFARate || rec.Wilson95Upper > MaxWilson95Upper || rec.N < MinN || rec.WarmP95Ms > MaxWarmP95Ms {
		return false, ReasonEvalFailed
	}
	return true, ""
}

// EvalGatePath returns $WATER_HOME/router/tier1_eval.json for the given
// water home directory.
func EvalGatePath(home string) string {
	return filepath.Join(home, "router", "tier1_eval.json")
}

// ReadEvalRecord loads the eval-gate record from disk. It returns an error
// (wrapping os.ErrNotExist when the file is absent) that the caller should
// treat as "eval_missing".
func ReadEvalRecord(home string) (EvalRecord, error) {
	b, err := os.ReadFile(EvalGatePath(home))
	if err != nil {
		return EvalRecord{}, err
	}
	var rec EvalRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return EvalRecord{}, fmt.Errorf("parse %s: %w", EvalGatePath(home), err)
	}
	return rec, nil
}

// WriteEvalRecord writes rec to $WATER_HOME/router/tier1_eval.json,
// creating the router directory if needed, via a temp-file-plus-rename so a
// reader never observes a partially written file.
func WriteEvalRecord(home string, rec EvalRecord) error {
	dir := filepath.Join(home, "router")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", dir, err)
	}
	b, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal eval record: %w", err)
	}
	path := EvalGatePath(home)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s to %s: %w", tmp, path, err)
	}
	return nil
}
