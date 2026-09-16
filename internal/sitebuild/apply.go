package sitebuild

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"water/internal/tools"
)

// PlanRow is one file in the approval plan, after the policy has resolved and
// accepted its destination.
type PlanRow struct {
	Path     string // path as the renderer named it, relative
	Resolved string // absolute destination the policy resolved it to
	Bytes    int
	Exists   bool
	WasBytes int64
}

// PlanTable is the complete set of effects a build-site run wants to apply.
type PlanTable struct {
	Role      string
	Intent    string
	Workspace string
	Rows      []PlanRow
}

// Overwrites counts rows that would replace an existing file.
func (t PlanTable) Overwrites() int {
	n := 0
	for _, r := range t.Rows {
		if r.Exists {
			n++
		}
	}
	return n
}

// Inspect validates every file against the policy before any of them is
// written, and gathers what the person needs to see to decide. A single
// refusal fails the whole plan: a partially-written site is worse than none.
func Inspect(pol *tools.Policy, files []GeneratedFile) (PlanTable, error) {
	t := PlanTable{Role: "build-site"}
	if len(pol.Filesystem.Roots) > 0 {
		t.Workspace = pol.Filesystem.Roots[0]
	}
	for _, f := range files {
		dec, out := pol.Authorize(tools.ToolWriteFile, map[string]any{"path": f.Path, "content": f.Content})
		if !dec.Allowed {
			return PlanTable{}, fmt.Errorf("refusing to write %s: %s", f.Path, dec.Basis)
		}
		resolved, _ := out["path"].(string)
		row := PlanRow{Path: f.Path, Resolved: resolved, Bytes: len(f.Content)}
		if fi, err := os.Stat(resolved); err == nil && !fi.IsDir() {
			row.Exists, row.WasBytes = true, fi.Size()
		}
		t.Rows = append(t.Rows, row)
	}
	return t, nil
}

// ApprovalText renders the one plan a person approves, in the same shape the
// interactive chat approval uses.
func ApprovalText(t PlanTable) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "REVIEW %d ACTIONS · %s\n\n", len(t.Rows), strings.ToUpper(t.Role))
	if t.Intent != "" {
		fmt.Fprintf(&sb, "Intent (ceo): %s\n", clean(t.Intent))
	}
	fmt.Fprintf(&sb, "Workspace: %s\n", t.Workspace)
	sb.WriteString("Scope: workspace only; no network, no shell.\n\n")
	for i, r := range t.Rows {
		fmt.Fprintf(&sb, "  %d. write %s (%d bytes)", i+1, r.Path, r.Bytes)
		if r.Exists {
			fmt.Fprintf(&sb, " — OVERWRITES %d bytes", r.WasBytes)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("\nApprove this plan once. Stop on failure; completed actions are not rolled back.\n")
	return sb.String()
}

// Confirm shows the plan and waits for an explicit yes. The default is no:
// silence, EOF, or anything unrecognised declines.
func Confirm(in io.Reader, out io.Writer, t PlanTable, yes bool) bool {
	fmt.Fprint(out, ApprovalText(t))
	if yes {
		fmt.Fprintln(out, "approved by --yes")
		return true
	}
	fmt.Fprintf(out, "apply these %d writes? [y/N] ", len(t.Rows))
	line, _ := bufio.NewReader(in).ReadString('\n')
	ans := strings.ToLower(strings.TrimSpace(line))
	return ans == "y" || ans == "yes"
}

// Apply writes the approved files through the tool service, so each one is
// authorised again and written atomically inside the declared root.
func Apply(ctx context.Context, svc *tools.Service, files []GeneratedFile) error {
	if svc == nil {
		return errors.New("no tool service")
	}
	for _, f := range files {
		if _, err := svc.Call(ctx, tools.ToolWriteFile, map[string]any{"path": f.Path, "content": f.Content}); err != nil {
			return fmt.Errorf("writing %s: %w", f.Path, err)
		}
	}
	return nil
}
