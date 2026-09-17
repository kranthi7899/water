package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"water/internal/agent"
	"water/internal/backend"
	"water/internal/chat"
	"water/internal/config"
	"water/internal/orchestrator"
	"water/internal/surface"
	"water/internal/tools"
	"water/internal/trace"
)

func (a *App) buildSiteCmd() *cobra.Command {
	var out string
	var timeout time.Duration
	c := &cobra.Command{
		Use:   "build-site [brief]",
		Short: "Ask the Design agent to build a web page, approve it once, and open it",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.config()
			if err != nil {
				return err
			}
			reg, err := a.roleRegistry()
			if err != nil {
				return err
			}
			role, ok := reg.Get("design")
			if !ok {
				return exitWith(ExitUsage, errors.New("build-site needs the design role"))
			}
			folder, err := siteFolder(out)
			if err != nil {
				return exitWith(ExitUsage, err)
			}
			workspace, err := os.Getwd()
			if err != nil {
				return err
			}

			in := bufio.NewReader(os.Stdin)
			brief := ""
			switch {
			case len(args) == 1:
				brief = args[0]
			case isTTY(os.Stdin):
				fmt.Fprint(os.Stderr, surface.StyleBold.Render("What should Water build? "))
				brief, _ = in.ReadString('\n')
			default:
				if brief, err = readStdin(); err != nil {
					return exitWith(ExitUsage, err)
				}
			}
			if brief = strings.TrimSpace(brief); brief == "" {
				return exitWith(ExitUsage, errors.New("empty brief"))
			}

			ctx := context.Background()
			def, err := a.selectBackend(ctx)
			if err != nil {
				return err
			}
			if w := meteredLeakWarning(def); w != "" {
				fmt.Fprintln(os.Stderr, "warning:", w)
			}
			env, _, err := a.roleEnv(ctx, reg, def)
			if err != nil {
				return exitWith(ExitBackend, err)
			}
			approvals, err := tools.NewApprovalBroker()
			if err != nil {
				return exitWith(ExitError, fmt.Errorf("local approvals are unavailable, so nothing can be written: %w", err))
			}
			defer approvals.Close()
			// The same bounded workspace as interactive chat, minus the shell:
			// building a page needs reads, writes and one open, nothing else.
			pol := tools.InteractiveWorkspacePolicy(role.Slug, role.RoleID, workspace, config.Home(), approvals.Socket())
			pol.Shell = tools.ShellPolicy{}
			local := make(map[string]*tools.Policy, len(env.RoleTools)+1)
			for k, v := range env.RoleTools {
				local[k] = v
			}
			local[role.Slug] = pol
			env.RoleTools = local
			// The call includes the time a person spends reading the plan.
			env.Timeout = max(env.Timeout, timeout)

			runID := orchestrator.NewRunID()
			rec, err := trace.New(cfg.Telemetry.TraceDir, runID)
			if err != nil {
				return err
			}
			env.Trace = rec
			rec.RunStarted(brief)
			rec.NodeStarted(role.Slug)
			fmt.Fprintf(os.Stderr, "%s\n%s  %s\n%s  %s\n\n", surface.Rule(),
				surface.StyleDim.Render("water"), surface.StyleAccent.Render("build-site · design"),
				surface.StyleDim.Render("brief"), surface.StyleInk.Render(brief))

			prompt := fmt.Sprintf("%s\n\nPut the site in the folder %s/ of this workspace. When it is done, reply to the user in two sentences at most.", brief, folder)
			type result struct {
				resp backend.Response
				err  error
			}
			done := make(chan result, 1)
			go func() {
				resp, _, err := agent.RunTurn(ctx, role, env, nil, prompt, nil)
				done <- result{resp, err}
			}()

			live := isTTY(os.Stderr)
			start := time.Now()
			poll := time.NewTicker(250 * time.Millisecond)
			defer poll.Stop()
			frames := []string{"◐", "◓", "◑", "◒"}
			var res result
			for n := 0; ; n++ {
				select {
				case res = <-done:
				case <-poll.C:
					if p, ok := approvals.Next(); ok {
						clearStatus(live)
						p.Decide(reviewPlan(in, os.Stderr, p.Request, workspace, a.flags.yes))
						fmt.Fprintln(os.Stderr)
					} else if live {
						fmt.Fprintf(os.Stderr, "\r\033[K  %s design is working · %s", surface.StyleAccent.Render(frames[n%len(frames)]), time.Since(start).Round(time.Second))
					}
					continue
				}
				break
			}
			clearStatus(live)

			rec.NodeFinished(role.Slug, res.resp.Duration, res.err)
			_ = backend.SaveRateLimit(config.Home(), res.resp.RateLimit)
			if res.err != nil {
				rec.Error(res.err)
				rec.Finish()
				if errors.Is(res.err, backend.ErrRateLimited) {
					return exitWith(ExitRateLimited, fmt.Errorf("subscription rate limit reached, not a failure: %w", res.err))
				}
				return exitWith(ExitBackend, fmt.Errorf("%w (a longer --timeout may help)", res.err))
			}
			stats := rec.Finish()

			page, opened := sitePage(res.resp.ToolEvents)
			reply := res.resp.Text
			if !a.flags.verbose {
				reply = firstParagraph(reply)
			}
			if page == "" {
				if planDeclined(res.resp.ToolEvents) {
					fmt.Fprintln(os.Stderr, "declined; nothing was written")
					return nil
				}
				fmt.Fprintln(os.Stderr, chat.SafeApprovalText(strings.TrimSpace(res.resp.Text)))
				return exitWith(ExitError, errors.New("design did not write a page"))
			}
			fmt.Fprintf(os.Stderr, "  %s built in %s · %s · metered %d\n\n", surface.StyleOK.Render("✓"),
				time.Since(start).Round(time.Second), res.resp.Backend, stats.MeteredCalls)
			if reply != "" {
				fmt.Fprintf(os.Stderr, "%s\n\n", chat.SafeApprovalText(reply))
			}
			if !opened {
				fmt.Fprintln(os.Stderr, surface.StyleDim.Render("the page was written but not opened; open it here:"))
			}
			fmt.Println(fileURL(page))
			return nil
		},
	}
	c.Flags().StringVar(&out, "out", "site", "folder inside the current directory to build the site in")
	c.Flags().DurationVar(&timeout, "timeout", 20*time.Minute, "longest the design turn may take, including your review of the plan")
	return c
}

// siteFolder accepts only a folder inside the current directory. The tool
// policy enforces the real boundary; this rejects a bad flag up front.
func siteFolder(out string) (string, error) {
	clean := filepath.Clean(strings.TrimSpace(out))
	if clean == "" || clean == "." || filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("--out must name a folder inside the current directory, got %q", out)
	}
	return filepath.ToSlash(clean), nil
}

func clearStatus(live bool) {
	if live {
		fmt.Fprint(os.Stderr, "\r\033[K")
	}
}

// reviewPlan shows one proposed plan and returns the person's decision.
// Anything but an explicit yes declines.
func reviewPlan(in *bufio.Reader, w io.Writer, req tools.ApprovalRequest, root string, yes bool) bool {
	fmt.Fprint(w, planText(req, root))
	if yes {
		fmt.Fprintln(w, "approved by --yes")
		return true
	}
	for {
		fmt.Fprint(w, "approve this plan? [y/N, d for file contents] ")
		line, err := in.ReadString('\n')
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "y", "yes":
			return true
		case "d":
			fmt.Fprint(w, planDetails(req, root))
			if err == nil {
				continue
			}
		}
		return false
	}
}

func planActions(req tools.ApprovalRequest) []tools.PlannedAction {
	if len(req.Actions) > 0 {
		return req.Actions
	}
	return []tools.PlannedAction{{Tool: req.Tool, Args: req.Args}}
}

func planText(req tools.ApprovalRequest, root string) string {
	var sb strings.Builder
	actions := planActions(req)
	fmt.Fprintf(&sb, "%s\n", surface.Rule())
	fmt.Fprintf(&sb, "%s\n", surface.StyleBold.Render(fmt.Sprintf("REVIEW %d ACTIONS · %s", len(actions), strings.ToUpper(req.Role))))
	if req.Summary != "" {
		fmt.Fprintf(&sb, "%s  %s\n", surface.StyleDim.Render("intent"), chat.SafeApprovalText(req.Summary))
	}
	fmt.Fprintf(&sb, "%s   %s\n", surface.StyleDim.Render("where"), root)
	fmt.Fprintf(&sb, "%s   workspace only · no shell · no network\n\n", surface.StyleDim.Render("scope"))
	opens := false
	for i, act := range actions {
		path, _ := act.Args["path"].(string)
		switch act.Tool {
		case tools.ToolWriteFile:
			content, _ := act.Args["content"].(string)
			note := ""
			if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
				note = "  " + surface.StyleWarn.Render("replaces existing file")
			}
			fmt.Fprintf(&sb, "  %d  write  %-28s %s%s\n", i+1, relPath(root, path), size(len(content)), note)
		case tools.ToolOpenPage:
			opens = true
			fmt.Fprintf(&sb, "  %d  open   %-28s in your browser\n", i+1, relPath(root, path))
		default:
			fmt.Fprintf(&sb, "  %d  %s\n", i+1, act.Tool)
		}
	}
	sb.WriteString("\n")
	if opens {
		sb.WriteString(surface.StyleDim.Render("The browser runs outside the sandbox, so Water opens the page only if it is self-contained: no scripts, no remote URLs.") + "\n")
	}
	sb.WriteString(surface.StyleDim.Render("One approval covers the whole plan. It stops on failure; finished steps are not rolled back.") + "\n")
	return sb.String()
}

func planDetails(req tools.ApprovalRequest, root string) string {
	var sb strings.Builder
	for _, act := range planActions(req) {
		if act.Tool != tools.ToolWriteFile {
			continue
		}
		path, _ := act.Args["path"].(string)
		content, _ := act.Args["content"].(string)
		fmt.Fprintf(&sb, "\n%s %s\n%s\n", surface.StyleDim.Render("──"), relPath(root, path), chat.SafeApprovalText(content))
	}
	return sb.String() + "\n"
}

func relPath(root, path string) string {
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	if rel, err := filepath.Rel(root, path); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return rel
	}
	return path
}

func size(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}

// sitePage finds the page the approved plan produced: the one it opened, or
// else the index.html it wrote.
func sitePage(evs []tools.Event) (page string, opened bool) {
	for _, ev := range evs {
		if ev.Allowed && ev.Error == "" && ev.Tool == tools.ToolOpenPage {
			if p, _ := ev.Args["path"].(string); p != "" {
				return p, true
			}
		}
	}
	for _, ev := range evs {
		if p, _ := ev.Args["path"].(string); ev.Allowed && ev.Error == "" && ev.Tool == tools.ToolWriteFile && strings.EqualFold(filepath.Base(p), "index.html") {
			page = p
		}
	}
	return page, false
}

func planDeclined(evs []tools.Event) bool {
	for _, ev := range evs {
		if ev.Tool == tools.ToolApplyActions && !ev.Allowed && strings.Contains(ev.Basis, "denied") {
			return true
		}
	}
	return false
}

var evidenceRef = regexp.MustCompile(`\s*\(evidence: [^)]*\)`)

// firstParagraph keeps the stage output to the agent's opening sentences;
// --verbose shows the whole reply.
func firstParagraph(text string) string {
	text = strings.TrimSpace(evidenceRef.ReplaceAllString(text, ""))
	if i := strings.Index(text, "\n\n"); i >= 0 {
		text = text[:i]
	}
	return strings.TrimSpace(text)
}

func fileURL(path string) string {
	return (&url.URL{Scheme: "file", Path: path}).String()
}
