package tools

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"
)

// Sandbox wraps a shell command in the strongest OS-level confinement this
// platform offers (Part 5.3). Guarantees, honestly:
//
//   - macOS: `sandbox-exec` with a generated profile — default deny, read
//     access to the declared roots (and the system libraries a shell needs),
//     write access only under read-write roots, no network. sandbox-exec is
//     deprecated by Apple but still functional; TCC-protected folders
//     (Desktop, Documents, Downloads) additionally require the user's TCC
//     consent for the *parent* process, which Water cannot grant itself.
//   - Linux: no kernel confinement is applied by this build. Landlock is the
//     right fit (unprivileged, path-based) and is the planned next step;
//     bubblewrap would require a separate binary, which violates the 3F
//     one-binary promise unless opt-in. Until then, Linux shell tools rely on
//     the allowlist alone. Say so in docs; do not imply otherwise.
//   - Windows: no confinement. Realistically weaker; documented as such.
//
// No role enables shell in v1, so this is exercised by tests only. Containers
// are strictly opt-in and not part of the default path.
// SandboxAvailable reports whether this platform can confine a shell
// command. Shell tools are refused where it cannot.
var SandboxAvailable = func() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	_, err := exec.LookPath("sandbox-exec")
	return err == nil
}

func Sandbox(cmd *exec.Cmd, p *Policy) *exec.Cmd {
	if runtime.GOOS != "darwin" {
		return cmd
	}
	if _, err := exec.LookPath("sandbox-exec"); err != nil {
		return cmd
	}
	profile := SandboxProfile(p)
	wrapped := exec.CommandContext(cmdContext(cmd), "sandbox-exec", append([]string{"-p", profile, cmd.Path}, cmd.Args[1:]...)...)
	wrapped.Dir, wrapped.Env, wrapped.Stdin = cmd.Dir, cmd.Env, cmd.Stdin
	return wrapped
}

// SandboxProfile renders the macOS Seatbelt profile for a policy.
func SandboxProfile(p *Policy) string {
	var sb strings.Builder
	sb.WriteString("(version 1)\n(deny default)\n")
	sb.WriteString("(allow process-exec process-fork)\n")
	sb.WriteString("(allow sysctl-read)\n")
	sb.WriteString("(allow file-read* (subpath \"/usr\") (subpath \"/bin\") (subpath \"/sbin\") (subpath \"/System\") (subpath \"/Library\") (subpath \"/private/etc\") (subpath \"/dev\") (literal \"/\"))\n")
	for _, r := range p.Filesystem.Roots {
		fmt.Fprintf(&sb, "(allow file-read* (subpath %q))\n", r)
		if p.Filesystem.Mode == "read-write" {
			fmt.Fprintf(&sb, "(allow file-write* (subpath %q))\n", r)
		}
	}
	sb.WriteString("(deny network*)\n")
	return sb.String()
}
