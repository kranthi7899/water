package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"water"
	"water/internal/config"
	"water/internal/nervous"
	"water/internal/nervous/eval"
	"water/internal/nervous/intents"
	"water/internal/nervous/promote"
	"water/internal/nervous/sidecar"
	"water/internal/twins"
)

// routeCmd is the top-level `water route` command group: `eval --tier1`
// (task R-18), `candidates` (task R-22) and `report` (task R-26).
func (a *App) routeCmd() *cobra.Command {
	c := &cobra.Command{Use: "route", Short: "The sous chef's routing: live evaluation and the growth loop"}
	c.AddCommand(a.routeEvalCmd())
	c.AddCommand(a.routeCandidatesCmd())
	c.AddCommand(a.routeReportCmd())
	return c
}

// routeReportCmd implements `water route report [--since 7d] [--json]`:
// GET /v1/route/report, the same route_log aggregation nervous.BuildReport
// (R-14) computes — tier distribution, escalation reasons, latency
// percentiles and recent possible misses.
func (a *App) routeReportCmd() *cobra.Command {
	var since string
	c := &cobra.Command{
		Use:   "report",
		Short: "Summarize recent routing: tiers, escalations, latency, possible misses",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := parseSinceFlag(since)
			if err != nil {
				return exitWith(ExitUsage, err)
			}
			dc, err := newDaemonClient()
			if err != nil {
				return exitWith(ExitError, err)
			}
			rep, err := dc.RouteReport(cmd.Context(), d)
			if err != nil {
				return exitWith(ExitError, err)
			}
			if a.jsonMode() {
				return printJSON(rep)
			}
			fmt.Print(renderRouteReport(rep))
			return nil
		},
	}
	c.Flags().StringVar(&since, "since", "7d", "lookback window: a plain Go duration (168h) or an <N>d shorthand (7d)")
	return c
}

// renderRouteReport formats a nervous.Report for a human terminal.
func renderRouteReport(rep nervous.Report) string {
	var b strings.Builder
	if rep.N == 0 {
		return styleDim.Render("no route_log rows in this window") + "\n"
	}
	fmt.Fprintf(&b, "%s %d\n", styleDim.Render("turns          "), rep.N)
	fmt.Fprintf(&b, "%s %s\n", styleDim.Render("tiers          "), formatCounts(rep.TierCounts))
	fmt.Fprintf(&b, "%s %s\n", styleDim.Render("owners         "), formatCounts(rep.OwnerCounts))
	fmt.Fprintf(&b, "%s %s\n", styleDim.Render("outcomes       "), formatCounts(rep.OutcomeCounts))
	if len(rep.EscalationReasons) > 0 {
		fmt.Fprintf(&b, "%s %s\n", styleDim.Render("escalations    "), formatCounts(rep.EscalationReasons))
	}
	fmt.Fprintf(&b, "%s total p50=%s p95=%s  ack p50=%s p95=%s\n",
		styleDim.Render("latency        "), rep.TotalP50, rep.TotalP95, rep.AckP50, rep.AckP95)
	if len(rep.QuickToolUsageCounts) > 0 {
		fmt.Fprintf(&b, "%s %s\n", styleDim.Render("quick tools    "), formatCounts(rep.QuickToolUsageCounts))
	}
	fmt.Fprintf(&b, "%s %d\n", styleDim.Render("possible misses"), rep.PossibleMissCount)
	if len(rep.InactiveIntentNearMiss) > 0 {
		fmt.Fprintf(&b, "%s %s\n", styleDim.Render("inactive hits  "), formatCounts(rep.InactiveIntentNearMiss))
	}
	return b.String()
}

// formatCounts renders a count map as "key=n, key=n, ...", sorted by key for
// stable output.
func formatCounts(m map[string]int) string {
	if len(m) == 0 {
		return "(none)"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return strings.Join(parts, ", ")
}

// routeCandidatesCmd implements `water route candidates [--since 30d]
// [--min 5] [--json]`: GET /v1/route/candidates, always available
// regardless of router.promotion.enabled (Design §16 item 1 — listing is
// read-only over route_log; only drafting/promoting one, R-23, is gated).
func (a *App) routeCandidatesCmd() *cobra.Command {
	var since string
	var minRepeats int
	c := &cobra.Command{
		Use:   "candidates",
		Short: "List repeated quick-tool patterns that could become a learned intent",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := parseSinceFlag(since)
			if err != nil {
				return exitWith(ExitUsage, err)
			}
			dc, err := newDaemonClient()
			if err != nil {
				return exitWith(ExitError, err)
			}
			cands, err := dc.RouteCandidates(cmd.Context(), d, minRepeats)
			if err != nil {
				return exitWith(ExitError, err)
			}
			if a.jsonMode() {
				return printJSON(cands)
			}
			fmt.Print(renderCandidates(cands))
			return nil
		},
	}
	c.Flags().StringVar(&since, "since", "30d", "lookback window: a plain Go duration (168h) or an <N>d shorthand (30d)")
	c.Flags().IntVar(&minRepeats, "min", promote.DefaultMinRepeats, "minimum distinct turns required for a candidate")
	return c
}

// parseSinceFlag accepts either a plain Go duration string ("168h") or the
// "<N>d" days shorthand docs/slices/R.md's own examples use throughout
// (`--since 30d`, `--since 7d`) for every route/report/candidates --since
// flag — GET /v1/route/candidates itself only ever parses a real Go
// duration (time.ParseDuration), so this is where the friendly "d" suffix
// gets translated before the request goes out.
func parseSinceFlag(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days <= 0 {
			return 0, fmt.Errorf("invalid --since %q (want e.g. 30d or 168h)", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("invalid --since %q (want e.g. 30d or 168h)", s)
	}
	return d, nil
}

// renderCandidates formats a promotion-candidate table for a human
// terminal, or a one-line message when there are none.
func renderCandidates(cands []promote.Candidate) string {
	if len(cands) == 0 {
		return styleDim.Render("no promotion candidates in this window") + "\n"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%-14s %-8s %-6s  %s\n", "ID", "REPEATS", "DAYS", "SIGNATURE")
	for _, c := range cands {
		fmt.Fprintf(&b, "%-14s %-8d %-6d  %s\n", c.ID, c.Repeats, c.Days, c.Signature)
	}
	return b.String()
}

// routeEvalCmd implements `water route eval --tier1`: the Tier 1
// (FunctionGemma) live eval-gate mechanism docs/slices/R.md §10 describes.
// It starts its own llama-server sidecar against the twin's currently
// installed model, runs the combined held-out eval set through the real
// Tier 1 adapter, and writes the resulting record to
// $WATER_HOME/router/tier1_eval.json — the same file the daemon consults
// before ever enabling Tier 1 for real turns. Nothing about this command
// runs implicitly: it is a network-free, read-your-own-disk operation the
// owner runs by hand, same posture as `water model pull`.
func (a *App) routeEvalCmd() *cobra.Command {
	var tier1 bool
	var llamaBin string
	c := &cobra.Command{
		Use:   "eval",
		Short: "Run the sous chef's eval harness",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !tier1 {
				return exitWith(ExitUsage, errors.New("water route eval currently only supports --tier1"))
			}

			home := config.Home()
			modelPath := filepath.Join(home, "models", sidecar.ModelFileName)
			if _, err := os.Stat(modelPath); err != nil {
				return exitWith(ExitUsage, fmt.Errorf("functiongemma model not found at %s; run `water model pull functiongemma --accept-gemma-terms` first", modelPath))
			}

			m, err := twins.Load(water.TwinsFS(), a.twinID())
			if err != nil {
				return exitWith(ExitError, fmt.Errorf("twin manifest: %w", err))
			}
			reg, err := intents.LoadRegistry(water.TwinsFS(), m, intentFunctions(), intents.LoadOptions{})
			if err != nil {
				return exitWith(ExitError, fmt.Errorf("intent registry: %w", err))
			}

			rec, report, err := eval.RunTier1Live(cmd.Context(), eval.Tier1LiveOptions{
				Home:      home,
				LlamaBin:  llamaBin,
				ModelPath: modelPath,
				Registry:  reg,
			})
			if err != nil {
				return exitWith(ExitError, err)
			}

			ok, reason := rec.Check(rec.ModelSHA256, rec.RegistryHash)

			if a.jsonMode() {
				return printJSON(map[string]any{
					"pass": ok, "reason": reason,
					"n": rec.N, "false_accepts": rec.FalseAccepts,
					"fa_rate": rec.FARate, "wilson95_upper": rec.Wilson95Upper,
					"warm_p95_ms": rec.WarmP95Ms, "at": rec.At,
					"hit_rate": report.HitRate, "intent_acc": report.IntentAcc,
					"escalation_rate": report.EscalationRate, "reasoning_answered": report.ReasoningAnswered,
					"record_path": sidecar.EvalGatePath(home),
				})
			}

			fmt.Printf("%s %d cases, %d false accepts (fa_rate %.4f, wilson95 upper %.4f), warm p95 %dms\n",
				styleDim.Render("tier1 eval"), rec.N, rec.FalseAccepts, rec.FARate, rec.Wilson95Upper, rec.WarmP95Ms)
			fmt.Printf("%s hit_rate=%.3f intent_acc=%.3f escalation_rate=%.3f reasoning_answered=%d\n",
				styleDim.Render("           "), report.HitRate, report.IntentAcc, report.EscalationRate, report.ReasoningAnswered)
			fmt.Printf("%s %s\n", styleDim.Render("record    "), sidecar.EvalGatePath(home))
			if ok {
				fmt.Println(styleAccent.Render("gate: PASS") + " — Tier 1 may be enabled (router.tier1.enabled)")
			} else {
				fmt.Println(styleWarn.Render("gate: FAIL")+" — reason:", reason)
				return exitWith(ExitError, fmt.Errorf("tier1 eval gate did not pass (%s)", reason))
			}
			return nil
		},
	}
	c.Flags().BoolVar(&tier1, "tier1", false, "run the Tier 1 (FunctionGemma) live eval")
	c.Flags().StringVar(&llamaBin, "llama-bin", "llama-server", "llama-server executable to use for the live eval sidecar")
	return c
}
