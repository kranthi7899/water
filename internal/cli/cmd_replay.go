package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"water/internal/backend"
	"water/internal/trace"
)

// replayCmd re-runs ONE recorded model call in isolation (Part 6a), with the
// exact system and prompt that node sent, optionally edited, without touching
// the run's state, checkpoint or any role's memory.
func (a *App) replayCmd() *cobra.Command {
	var printOnly, edit bool
	c := &cobra.Command{
		Use:   "replay <run-id> [seq | role]",
		Short: "Re-run one recorded node call in isolation, optionally with edited input",
		Long: `Lists a run's recorded calls, or re-sends one of them to a backend exactly as
the node sent it. --print shows the model-visible input without calling
anything; --edit opens it in $EDITOR first. Tools and attachments are not
replayed (a tool read cannot be reproduced faithfully); the replay says so.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			runID := filepath.Base(args[0])
			dir := trace.CallsDirFor(cfg.Telemetry.TraceDir, runID)
			recs, err := loadCallRecords(dir)
			if err != nil {
				return exitWith(ExitUsage, fmt.Errorf("no recorded calls for %s in %s (runs before call recording was added have none): %w", runID, dir, err))
			}
			if len(args) == 1 {
				if a.jsonMode() {
					return printJSON(recs)
				}
				for _, r := range recs {
					status := "ok"
					if r.Error != "" {
						status = "error: " + truncateLine(r.Error, 60)
					}
					fmt.Printf("%3d  %-7s %-20s %6dms  in=%-6d skills=%-3d inbox=%-3d %s\n", r.Seq, r.Role, r.Backend, r.DurationMS, r.InputTokens, len(r.Skills), len(r.InboxIDs), status)
				}
				return nil
			}
			rec, err := pickRecord(recs, args[1])
			if err != nil {
				return exitWith(ExitUsage, err)
			}
			if printOnly {
				fmt.Printf("# call %d · %s · %s\n\n## SYSTEM\n\n%s\n\n## PROMPT\n\n%s\n", rec.Seq, rec.Role, rec.Backend, rec.System, rec.Prompt)
				return nil
			}
			system, prompt := rec.System, rec.Prompt
			if edit {
				if system, prompt, err = editRecord(rec); err != nil {
					return exitWith(ExitUsage, err)
				}
			}
			if len(rec.Tools) > 0 || len(rec.Attachments) > 0 {
				fmt.Fprintf(os.Stderr, "note: the original call had tools %v and attachments %v; the replay sends neither\n", rec.Tools, rec.Attachments)
			}
			ctx := context.Background()
			sel, err := a.selectBackend(ctx)
			if err != nil {
				return err
			}
			if a.flags.backend == "" && rec.Backend != "" {
				if b, ok := backend.Default.Get(rec.Backend); ok && b.Available(ctx).Usable() {
					sel.Backend = b
				}
			}
			start := time.Now()
			resp, runErr := sel.Backend.Run(ctx, backend.Request{System: system, Prompt: prompt, Role: rec.Role, Model: rec.Model, Timeout: cfg.CallTimeoutDuration()})
			out := trace.CallRecord{Seq: 0, RunID: runID, Role: rec.Role, At: start, Backend: sel.Backend.Name(), Model: rec.Model, System: system, Prompt: prompt,
				Response: resp.Text, DurationMS: time.Since(start).Milliseconds(), ReplayOf: rec.Seq, InputTokens: resp.InputTokens, OutputTokens: resp.OutputTokens}
			if runErr != nil {
				out.Error = runErr.Error()
			}
			name := fmt.Sprintf("replay-%03d-%s-%s.json", rec.Seq, rec.Role, start.UTC().Format("20060102T150405"))
			if b, err := json.MarshalIndent(out, "", "  "); err == nil {
				_ = os.WriteFile(filepath.Join(dir, name), b, 0o600)
			}
			if a.jsonMode() {
				return printJSON(map[string]any{"original": rec, "replay": out, "input_edited": system != rec.System || prompt != rec.Prompt})
			}
			fmt.Fprintf(os.Stderr, "replayed call %d (%s) via %s in %s · input edited: %v · saved %s\n", rec.Seq, rec.Role, out.Backend,
				time.Duration(out.DurationMS)*time.Millisecond, system != rec.System || prompt != rec.Prompt, name)
			if runErr != nil {
				return exitWith(ExitBackend, runErr)
			}
			fmt.Println("=== original response")
			fmt.Println(strings.TrimSpace(rec.Response))
			fmt.Println("\n=== replay response")
			fmt.Println(strings.TrimSpace(resp.Text))
			return nil
		},
	}
	c.Flags().BoolVar(&printOnly, "print", false, "print the model-visible input for the call and exit")
	c.Flags().BoolVar(&edit, "edit", false, "edit system and prompt in $EDITOR before replaying")
	return c
}

func loadCallRecords(dir string) ([]trace.CallRecord, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []trace.CallRecord
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), "replay-") || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var r trace.CallRecord
		if json.Unmarshal(b, &r) == nil {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("directory holds no call records")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// pickRecord selects by sequence number, or the LAST call made by a role.
func pickRecord(recs []trace.CallRecord, sel string) (trace.CallRecord, error) {
	if n, err := strconv.Atoi(sel); err == nil {
		for _, r := range recs {
			if r.Seq == n {
				return r, nil
			}
		}
		return trace.CallRecord{}, fmt.Errorf("no call %d in this run", n)
	}
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].Role == strings.ToLower(sel) {
			return recs[i], nil
		}
	}
	return trace.CallRecord{}, fmt.Errorf("no call by role %q in this run", sel)
}

const editSeparator = "\n===== PROMPT (everything below this line is the user prompt) =====\n"

func editRecord(rec trace.CallRecord) (string, string, error) {
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		return "", "", errors.New("$EDITOR is not set")
	}
	f, err := os.CreateTemp("", "water-replay-*.md")
	if err != nil {
		return "", "", err
	}
	defer os.Remove(f.Name())
	_ = os.Chmod(f.Name(), 0o600)
	_, _ = f.WriteString(rec.System + editSeparator + rec.Prompt)
	f.Close()
	parts := strings.Fields(ed)
	c := exec.Command(parts[0], append(parts[1:], f.Name())...)
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		return "", "", err
	}
	b, err := os.ReadFile(f.Name())
	if err != nil {
		return "", "", err
	}
	system, prompt, ok := strings.Cut(string(b), editSeparator)
	if !ok {
		return "", "", errors.New("the PROMPT separator line was removed; keep it to split system from prompt")
	}
	return system, prompt, nil
}
