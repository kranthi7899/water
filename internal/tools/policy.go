// Package tools is Water's tool layer: the twin's connector functions,
// exposed to the model as MCP tools and proxied to the daemon's gate, which
// is the only place that actually decides whether a call may run.
//
// Nothing in this package trusts the working directory or the caller's
// argument as-is; ResolveWithinRoots exists for any future connector that
// needs to resolve a path against explicitly declared roots rather than the
// process's cwd.
package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Policy is what one MCP-serve child is handed by path: the twin's connector
// functions available this turn, and how to reach the daemon's gate to
// actually run one.
type Policy struct {
	Role string `json:"role"`

	// Twin lists the manifest's connector functions exposed as tools for this
	// call, proxied to the daemon's gate at TwinSocket with TwinToken (a
	// long-lived, session-scoped token; the gate itself decides level,
	// taint, approval and rate caps, never this process).
	Twin       []TwinFunction `json:"twin,omitempty"`
	TwinSocket string         `json:"twin_socket,omitempty"`
	TwinToken  string         `json:"twin_token,omitempty"`
}

// ErrDenied is the base error for every permission refusal.
var ErrDenied = errors.New("denied by tool policy")

// Empty reports whether the policy grants no tools at all.
func (p *Policy) Empty() bool { return p == nil || len(p.Twin) == 0 }

// ToolNames lists the tools this policy exposes, sorted.
func (p *Policy) ToolNames() []string {
	if p.Empty() {
		return nil
	}
	out := make([]string, 0, len(p.Twin))
	for _, f := range p.Twin {
		out = append(out, f.Tool)
	}
	sort.Strings(out)
	return out
}

// Decision is the outcome of an authorisation check.
type Decision struct {
	Allowed bool   `json:"allowed"`
	Basis   string `json:"basis"` // human-readable rule that decided it
}

// Authorize decides whether tool may run. Every tool this policy can list is
// a twin function; actually deciding whether the call is allowed (level,
// taint, an approved envelope, rate caps) happens in the daemon's gate, not
// here — this only checks the call is structurally routable to it.
func (p *Policy) Authorize(tool string, args map[string]any) (Decision, map[string]any) {
	tf, ok := p.twinByTool(tool)
	if !ok {
		return Decision{false, "unknown tool " + tool}, args
	}
	if p.TwinSocket == "" || p.TwinToken == "" {
		return Decision{false, "twin tool proxy is not configured for this call"}, args
	}
	return Decision{true, "proxied to the daemon gate for " + tf.ID}, args
}

// ResolveWithinRoots resolves p to an absolute, symlink-free path and returns
// it with the root that contains it. Relative paths are resolved against the
// FIRST root, never against the process working directory. A path that
// escapes every root via .. or a symlink is refused. No current connector
// uses this yet; it is kept for one that resolves paths against declared
// roots (a future file-creation connector, Slice D).
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
