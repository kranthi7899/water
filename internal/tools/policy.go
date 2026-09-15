// Package tools is Water's own tool layer (Part 5, Option C via MCP).
//
// The subprocess model reasons and *requests*; Water parses the request,
// enforces the role's permissions, executes, traces, and returns. Nothing in
// this package trusts the working directory, the environment, or the caller:
// every path is resolved against explicitly declared roots, every call is
// logged with its permission decision, and a policy with nothing granted
// exposes zero tools.
package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// Tool names exposed over MCP (prefixed mcp__water__ on the claude side).
const (
	ToolReadFile  = "read_file"
	ToolListDir   = "list_dir"
	ToolWriteFile = "write_file"
	ToolRun       = "run"
	// ToolApplyActions is an explicit, bounded action plan. Interactive chat
	// exposes this instead of raw write/run calls so one human decision can
	// cover a reviewed set of related effects without becoming a broad grant.
	ToolApplyActions = "apply_actions"
)

// Policy is the resolved, role-scoped permission set. It is serialised to a
// 0600 file and handed to the MCP child process by path.
type Policy struct {
	Role   string `json:"role"`
	RoleID string `json:"role_id,omitempty"`
	RunID  string `json:"run_id,omitempty"`

	Filesystem FSPolicy    `json:"filesystem"`
	Shell      ShellPolicy `json:"shell"`
	Network    string      `json:"network"` // none
	// ApprovalSocket is a private session socket used only by an interactive
	// chat parent to approve one write or shell action. It is absent for
	// orchestration and headless runs, which therefore remain deny-by-default.
	ApprovalSocket string `json:"approval_socket,omitempty"`
	// BatchActions makes apply_actions the only consequential tool exposed.
	// It is used exclusively by interactive chat; headless policies retain
	// their declared per-tool surface and never gain a batch escape hatch.
	BatchActions bool `json:"batch_actions,omitempty"`
	// Trace is the narrow verification capability (Part 1 follow-up):
	// "current-run" lets the role resolve evidence references against the
	// trace of the run it is participating in. Not an MCP tool; it never
	// reaches the subprocess. "" = none.
	Trace string `json:"trace,omitempty"`

	// MaxReadBytes caps a single read (default 256 KiB).
	MaxReadBytes int64 `json:"max_read_bytes,omitempty"`

	// Protected paths are never readable or writable through tools, even when
	// they sit under a declared root. Water sets this to its own home, which
	// holds every role's memory, transcripts, traces and the signing keyring:
	// a root like "~" must not become a path around per-role isolation.
	Protected []string `json:"protected,omitempty"`
}

// FSPolicy is the filesystem grant.
type FSPolicy struct {
	Mode  string   `json:"mode"`  // none | read-only | read-write
	Roots []string `json:"roots"` // absolute, explicitly declared; never inherited
}

// ShellPolicy is the shell grant. No role uses shell in v1; the type exists so
// the policy shape is complete and the deny path is exercised.
type ShellPolicy struct {
	Mode      string   `json:"mode"` // none | allowlist | confirm-each | unrestricted
	Allowlist []string `json:"allowlist,omitempty"`
}

// ErrDenied is the base error for every permission refusal.
var ErrDenied = errors.New("denied by tool policy")

// HasTrace reports whether the role may resolve evidence references in its
// own run's trace.
func (p *Policy) HasTrace() bool { return p != nil && p.Trace == "current-run" }

// RequiresApproval identifies the actions with external effects. Reads and
// directory listings within an already-approved workspace do not prompt; a
// write or process launch always does.
func (p *Policy) RequiresApproval(tool string) bool {
	return p != nil && p.ApprovalSocket != "" && (tool == ToolWriteFile || tool == ToolRun || tool == ToolApplyActions)
}

// InteractiveWorkspacePolicy is the local-chat baseline: one explicit
// workspace is readable and writable, while every write and command waits for
// approval from the person at the terminal. It is never used for headless
// runs or orchestration.
func InteractiveWorkspacePolicy(role, roleID, workspace, protected, approvalSocket string) *Policy {
	return &Policy{
		Role:           role,
		RoleID:         roleID,
		ApprovalSocket: approvalSocket,
		BatchActions:   true,
		Filesystem:     FSPolicy{Mode: "read-write", Roots: []string{workspace}},
		Shell:          ShellPolicy{Mode: "confirm-each"},
		Network:        "none",
		Protected:      []string{protected},
	}
}

// Empty reports whether the policy grants no subprocess tools at all. Trace
// access is deliberately excluded: it is served in-process, never over MCP.
func (p *Policy) Empty() bool {
	if p == nil {
		return true
	}
	fs := p.Filesystem.Mode != "" && p.Filesystem.Mode != "none" && len(p.Filesystem.Roots) > 0
	sh := p.Shell.Mode != "" && p.Shell.Mode != "none"
	return !fs && !sh
}

// ToolNames lists the tools this policy exposes, sorted.
func (p *Policy) ToolNames() []string {
	if p.Empty() {
		return nil
	}
	var out []string
	switch p.Filesystem.Mode {
	case "read-only":
		if len(p.Filesystem.Roots) > 0 {
			out = append(out, ToolReadFile, ToolListDir)
		}
	case "read-write":
		if len(p.Filesystem.Roots) > 0 {
			out = append(out, ToolReadFile, ToolListDir)
			if !p.BatchActions {
				out = append(out, ToolWriteFile)
			}
		}
	}
	switch p.Shell.Mode {
	case "allowlist", "confirm-each", "unrestricted":
		if !p.BatchActions {
			out = append(out, ToolRun)
		}
	}
	if p.BatchActions && (p.Filesystem.Mode == "read-write" || p.Shell.Mode != "" && p.Shell.Mode != "none") {
		out = append(out, ToolApplyActions)
	}
	sort.Strings(out)
	return out
}

// Decision is the outcome of an authorisation check, recorded in the trace.
type Decision struct {
	Allowed bool   `json:"allowed"`
	Basis   string `json:"basis"` // human-readable rule that decided it
}

// Authorize decides whether tool may run with args under this policy. It
// resolves paths against roots (symlinks and .. included) and never consults
// the process working directory.
func (p *Policy) Authorize(tool string, args map[string]any) (Decision, map[string]any) {
	if p.Empty() {
		return Decision{false, "policy grants nothing"}, args
	}
	str := func(k string) string {
		v, _ := args[k].(string)
		return v
	}
	switch tool {
	case ToolApplyActions:
		if !p.BatchActions || p.ApprovalSocket == "" {
			return Decision{false, "action plans are available only in an interactive workspace session"}, args
		}
		return Decision{true, "interactive workspace action plan; every action will be validated before approval"}, args
	case ToolReadFile, ToolListDir:
		if p.Filesystem.Mode != "read-only" && p.Filesystem.Mode != "read-write" {
			return Decision{false, "filesystem.mode=" + orNone(p.Filesystem.Mode)}, args
		}
		resolved, root, err := ResolveWithinRoots(p.Filesystem.Roots, str("path"))
		if err != nil {
			return Decision{false, "path outside declared roots: " + err.Error()}, args
		}
		if prot, hit := p.protectedHit(resolved); hit {
			return Decision{false, "path is inside water's own state directory (" + prot + "), which holds other roles' memory and the keyring"}, args
		}
		out := cloneArgs(args)
		out["path"] = resolved
		return Decision{true, "filesystem.mode=" + p.Filesystem.Mode + " root=" + root}, out
	case ToolWriteFile:
		if p.BatchActions {
			return Decision{false, "interactive workspace requires apply_actions so related effects can be reviewed together"}, args
		}
		if p.Filesystem.Mode != "read-write" {
			return Decision{false, "filesystem.mode=" + orNone(p.Filesystem.Mode) + " (write requires read-write)"}, args
		}
		// The target may not exist yet: resolve its parent directory.
		target := str("path")
		parent, root, err := ResolveWithinRoots(p.Filesystem.Roots, filepath.Dir(target))
		if err != nil {
			return Decision{false, "parent outside declared roots: " + err.Error()}, args
		}
		if prot, hit := p.protectedHit(filepath.Join(parent, filepath.Base(target))); hit {
			return Decision{false, "path is inside water's own state directory (" + prot + ")"}, args
		}
		out := cloneArgs(args)
		out["path"] = filepath.Join(parent, filepath.Base(target))
		return Decision{true, "filesystem.mode=read-write root=" + root}, out
	case ToolRun:
		if p.BatchActions {
			return Decision{false, "interactive workspace requires apply_actions so related effects can be reviewed together"}, args
		}
		mode := orNone(p.Shell.Mode)
		if mode == "none" {
			return Decision{false, "shell.mode=none"}, args
		}
		// Codex allows commands no rule matched only where a platform sandbox
		// enforces the boundary, and never without one. Water has no approval
		// channel inside a tool call, so without a sandbox it refuses.
		if !SandboxAvailable() {
			return Decision{false, "shell.mode=" + mode + " refused: no OS sandbox on " + runtime.GOOS + ", so a command's effects cannot be confined"}, args
		}
		switch mode {
		case "unrestricted":
			return Decision{true, "shell.mode=unrestricted (confined by sandbox)"}, args
		case "confirm-each":
			if p.ApprovalSocket == "" {
				return Decision{false, "shell.mode=confirm-each needs an interactive approval channel, which this build does not have"}, args
			}
			return Decision{true, "shell.mode=confirm-each; awaiting user approval (confined by sandbox)"}, args
		case "allowlist":
			cmd := strings.TrimSpace(str("command"))
			// The allowlist names single simple commands. Chaining, pipes,
			// redirection and substitution would let "ls" authorise anything.
			if strings.ContainsAny(cmd, ";&|$`<>()\\\n\r") {
				return Decision{false, "shell.mode=allowlist: compound, piped, redirected or substituted command refused; the allowlist matches one simple command"}, args
			}
			fields := strings.Fields(cmd)
			if len(fields) == 0 {
				return Decision{false, "shell.mode=allowlist: empty command"}, args
			}
			for _, a := range p.Shell.Allowlist {
				if a == fields[0] || a == cmd {
					out := cloneArgs(args)
					out["argv"] = fields
					return Decision{true, "shell.mode=allowlist match " + a + " (argument effects confined by sandbox)"}, out
				}
			}
			return Decision{false, "shell.mode=allowlist: " + fields[0] + " not in allowlist"}, args
		}
		return Decision{false, "shell.mode=" + mode + " is not recognised"}, args
	}
	return Decision{false, "unknown tool " + tool}, args
}

// protectedHit reports the protected path that contains p, if any.
func (p *Policy) protectedHit(path string) (string, bool) {
	rp, err := realPath(path)
	if err != nil {
		rp = path
	}
	for _, prot := range p.Protected {
		pp, err := realPath(prot)
		if err != nil {
			continue
		}
		if rp == pp || strings.HasPrefix(rp, pp+string(filepath.Separator)) {
			return prot, true
		}
	}
	return "", false
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func cloneArgs(a map[string]any) map[string]any {
	out := make(map[string]any, len(a))
	for k, v := range a {
		out[k] = v
	}
	return out
}

// ResolveWithinRoots resolves p to an absolute, symlink-free path and returns
// it with the root that contains it. Relative paths are resolved against the
// FIRST root, never against the process working directory. A path that
// escapes every root via .. or a symlink is refused.
func ResolveWithinRoots(roots []string, p string) (resolved, root string, err error) {
	if strings.TrimSpace(p) == "" {
		return "", "", errors.New("empty path")
	}
	if len(roots) == 0 {
		return "", "", errors.New("no roots declared")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(roots[0], p)
	}
	abs, err := realPath(p)
	if err != nil {
		return "", "", err
	}
	for _, r := range roots {
		rr, rerr := realPath(r)
		if rerr != nil {
			continue
		}
		if abs == rr || strings.HasPrefix(abs, rr+string(filepath.Separator)) {
			return abs, r, nil
		}
	}
	return "", "", fmt.Errorf("%s is not under any declared root", abs)
}

// realPath cleans, absolutises, and resolves symlinks. If the leaf does not
// exist yet, the deepest existing ancestor is resolved and the remainder
// re-appended, so a write target is still checked against the real tree.
func realPath(p string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(p))
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		return r, nil
	}
	dir, base := filepath.Split(abs)
	dir = strings.TrimRight(dir, string(filepath.Separator))
	if dir == "" || dir == abs {
		return abs, nil
	}
	rd, err := realPath(dir)
	if err != nil {
		return "", err
	}
	return filepath.Join(rd, base), nil
}

// ExpandRoots absolutises and ~-expands declared roots, dropping empties.
func ExpandRoots(roots []string) []string {
	var out []string
	home, _ := os.UserHomeDir()
	for _, r := range roots {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if strings.HasPrefix(r, "~/") && home != "" {
			r = filepath.Join(home, r[2:])
		}
		if abs, err := filepath.Abs(r); err == nil {
			r = abs
		}
		out = append(out, r)
	}
	return out
}

// WritePolicyFile serialises the policy to a 0600 file and returns its path.
func WritePolicyFile(dir string, p *Policy) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "policy-"+p.Role+"-*.json")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := os.Chmod(f.Name(), 0o600); err != nil {
		return "", err
	}
	if err := json.NewEncoder(f).Encode(p); err != nil {
		return "", err
	}
	return f.Name(), nil
}

// LoadPolicyFile reads a policy written by WritePolicyFile.
func LoadPolicyFile(path string) (*Policy, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Policy
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("policy file %s: %w", path, err)
	}
	return &p, nil
}

// FromGrant resolves a role.yaml tools block plus the user's configured roots
// into a Policy. A nil grant yields an empty policy no matter what roots the
// user configured: roots widen only what a role already declares. Roots
// declared in role.yaml and in config are both expanded; the working
// directory is never implied.
func FromGrant(role, roleID string, grant any, configRoots []string) *Policy {
	p := &Policy{Role: role, RoleID: roleID, Network: "none"}
	g, ok := grant.(interface {
		FS() (mode string, roots []string)
		SH() (mode string, allow []string)
		NET() string
		TR() string
	})
	if !ok || grant == nil {
		return p
	}
	p.Trace = g.TR()
	mode, roots := g.FS()
	p.Filesystem.Mode = orNone(mode)
	if p.Filesystem.Mode != "none" {
		p.Filesystem.Roots = ExpandRoots(append(append([]string{}, roots...), configRoots...))
	}
	sm, allow := g.SH()
	p.Shell.Mode = orNone(sm)
	p.Shell.Allowlist = allow
	if n := g.NET(); n != "" {
		p.Network = n
	}
	return p
}
