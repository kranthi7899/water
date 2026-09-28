package decisions

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"water/internal/store"
)

// Figure is one computed value and where it came from. Source must name the
// code or tool result the value traces to ("code:runway_calc",
// "gdrive:<file id>"); the builder rejects a figure without one.
type Figure struct {
	Value  any
	Source string
}

// ComputeInput is what a compute function sees: the item the card is about
// and every need resolved before it, by need name, in YAML order.
type ComputeInput struct {
	Item     store.Record
	Resolved map[string]NeedResult
}

// ComputeResult is a computation's output. Figures become the card's
// Defaults; Evidence is added as-is. External marks a result derived from
// content written by someone else (e.g. a Drive spreadsheet), which makes
// the card Untrusted. A result with no figures and no evidence counts as
// empty (missing_info); returning an error counts as blocked.
type ComputeResult struct {
	Figures  map[string]Figure
	Evidence []Evidence
	External bool
}

func (r ComputeResult) empty() bool { return len(r.Figures) == 0 && len(r.Evidence) == 0 }

// ComputeFunc is an ordinary Go function behind an internal:// need. It
// must do its arithmetic in code; it is never a model completion.
type ComputeFunc func(ctx context.Context, in ComputeInput) (ComputeResult, error)

var (
	computeMu sync.RWMutex
	computes  = map[string]ComputeFunc{}
)

func computeKey(name string) string { return strings.TrimPrefix(name, InternalPrefix) }

// RegisterCompute makes fn available as internal://<name>. name may be
// given with or without the internal:// prefix. Call it from an init func
// in the decision type's own file, before LoadRegistry runs; a duplicate or
// empty name panics, as registering twice is a programming error.
func RegisterCompute(name string, fn ComputeFunc) {
	key := computeKey(name)
	if key == "" || fn == nil {
		panic("decisions: RegisterCompute needs a name and a function")
	}
	computeMu.Lock()
	defer computeMu.Unlock()
	if _, dup := computes[key]; dup {
		panic(fmt.Sprintf("decisions: compute function %s registered twice", key))
	}
	computes[key] = fn
}

func lookupCompute(name string) (ComputeFunc, bool) {
	computeMu.RLock()
	defer computeMu.RUnlock()
	fn, ok := computes[computeKey(name)]
	return fn, ok
}
