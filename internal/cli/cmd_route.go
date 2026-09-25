package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"water"
	"water/internal/config"
	"water/internal/nervous/eval"
	"water/internal/nervous/intents"
	"water/internal/nervous/sidecar"
	"water/internal/twins"
)

// routeCmd is the top-level `water route` command group. Only `eval
// --tier1` exists yet (task R-18); `report` and `candidates` are a later
// task's addition (docs/slices/R.md's R-26), once the route_log-backed
// report/candidates machinery they depend on exists.
func (a *App) routeCmd() *cobra.Command {
	c := &cobra.Command{Use: "route", Short: "The sous chef's routing: live evaluation"}
	c.AddCommand(a.routeEvalCmd())
	return c
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
