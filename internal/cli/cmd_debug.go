package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"water/internal/backend"
	"water/internal/orchestrator"
)

// activeNodes tracks nodes that started and have not finished, for dumps.
type activeNodes struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func newActiveNodes() *activeNodes { return &activeNodes{m: map[string]time.Time{}} }
func (a *activeNodes) start(role string) {
	a.mu.Lock()
	a.m[role] = time.Now()
	a.mu.Unlock()
}
func (a *activeNodes) finish(role string) {
	a.mu.Lock()
	delete(a.m, role)
	a.mu.Unlock()
}

// pidFile marks a live orchestration so `water debug dump` can find it.
func pidFile(checkpointDir, runID string) string {
	return filepath.Join(checkpointDir, runID+".pid")
}

// writeStateDump renders what a stuck run is doing: active nodes and how long
// each has run, the model subprocesses still in flight, what the router would
// schedule next, pending assignments, the tail of the outbox, and every
// goroutine's stack. It never blocks on the state mutex.
func writeStateDump(traceDir string, st *orchestrator.State, router orchestrator.Router, active *activeNodes) (string, error) {
	var b bytes.Buffer
	now := time.Now()
	fmt.Fprintf(&b, "water state dump · run %s · %s · pid %d\n\n", st.RunID, now.Format(time.RFC3339), os.Getpid())

	active.mu.Lock()
	var names []string
	for n := range active.m {
		names = append(names, n)
	}
	sort.Strings(names)
	b.WriteString("## Active nodes\n")
	if len(names) == 0 {
		b.WriteString("  (none — the executor is between steps or finished)\n")
	}
	for _, n := range names {
		fmt.Fprintf(&b, "  %-8s running for %s\n", n, now.Sub(active.m[n]).Round(time.Millisecond))
	}
	active.mu.Unlock()

	b.WriteString("\n## Model subprocesses in flight\n")
	calls := backend.InFlight()
	if len(calls) == 0 {
		b.WriteString("  (none)\n")
	}
	for _, c := range calls {
		fmt.Fprintf(&b, "  pid %-7d %s for %s\n      %s %s\n", c.PID, filepath.Base(c.Bin), now.Sub(c.Started).Round(time.Millisecond), c.Bin, c.Args)
	}

	snap, ok := st.TrySnapshot(2 * time.Second)
	b.WriteString("\n## State\n")
	if !ok {
		b.WriteString("  STATE MUTEX HELD FOR >2s — a goroutine is holding the run state lock; see the goroutine stacks below for who.\n")
	} else {
		fmt.Fprintf(&b, "  step %d · visits %v · consumed %v · untrusted %v · final written %v\n", snap.StepCount, snap.Visits, snap.Consumed, snap.Untrusted, snap.FinalOutput != nil)
		if router != nil {
			fmt.Fprintf(&b, "  router %s would schedule next: %v\n", router.Name(), router.Next(st))
			if h, isH := router.(*orchestrator.HierarchyRouter); isH {
				fmt.Fprintf(&b, "  specialists holding unanswered assignments: %v\n", h.PendingSpecialists(st))
			}
		}
		b.WriteString("\n## Outbox (last 10 of " + strconv.Itoa(len(snap.Outbox)) + ")\n")
		startIdx := len(snap.Outbox) - 10
		if startIdx < 0 {
			startIdx = 0
		}
		for _, m := range snap.Outbox[startIdx:] {
			flags := ""
			if m.Verbatim {
				flags += " verbatim"
			}
			if m.Untrusted {
				flags += " untrusted"
			}
			fmt.Fprintf(&b, "  %s %s → %s [%s]%s corr=%s  %s\n", m.ID, m.From, m.To, m.Topic, flags, m.CorrelationID, truncateLine(m.Payload, 90))
		}
	}

	b.WriteString("\n## Goroutines\n")
	_ = pprof.Lookup("goroutine").WriteTo(&b, 2)

	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(traceDir, fmt.Sprintf("%s.dump-%s.txt", st.RunID, now.UTC().Format("20060102T150405")))
	return path, os.WriteFile(path, b.Bytes(), 0o600)
}

func (a *App) debugCmd() *cobra.Command {
	c := &cobra.Command{Use: "debug", Short: "Debugging tools for runs (dump a live run's state)"}
	dump := &cobra.Command{
		Use:   "dump <run-id>",
		Short: "Dump a running orchestration's state and goroutines without stopping it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			runID := filepath.Base(args[0])
			raw, err := os.ReadFile(pidFile(cfg.Orchestration.CheckpointDir, runID))
			if err != nil {
				return exitWith(ExitUsage, fmt.Errorf("run %s is not live (no pid file); for a finished run use `water diagnose %s`", runID, runID))
			}
			var meta struct {
				PID int `json:"pid"`
			}
			if json.Unmarshal(raw, &meta) != nil || meta.PID == 0 {
				return exitWith(ExitUsage, errors.New("pid file is unreadable"))
			}
			before := time.Now()
			if err := signalDump(meta.PID); err != nil {
				return exitWith(ExitError, fmt.Errorf("signal pid %d: %w (the process may have died without cleaning up)", meta.PID, err))
			}
			deadline := time.Now().Add(6 * time.Second)
			for time.Now().Before(deadline) {
				matches, _ := filepath.Glob(filepath.Join(cfg.Telemetry.TraceDir, runID+".dump-*.txt"))
				for _, m := range matches {
					if fi, err := os.Stat(m); err == nil && fi.ModTime().After(before.Add(-time.Second)) {
						b, _ := os.ReadFile(m)
						if !strings.Contains(string(b), "## Goroutines") {
							continue
						}
						fmt.Fprintln(os.Stderr, "dump:", m)
						fmt.Print(string(b))
						return nil
					}
				}
				time.Sleep(150 * time.Millisecond)
			}
			return exitWith(ExitError, fmt.Errorf("pid %d did not write a dump within 6s", meta.PID))
		},
	}
	c.AddCommand(dump)
	return c
}
