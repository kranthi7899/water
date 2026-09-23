package cli

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"water/internal/config"
	"water/internal/identity"
	"water/internal/surface"
)

// personaCmd is gated behind a real local agents directory (invariant #5):
// end users of the binary cannot see or edit personas. Memory is NOT behind
// this gate — `water memory` is runtime state on every install.
func (a *App) personaCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "persona <show|edit|sign|verify> [role] [file]",
		Short: "Inspect, edit (with confirmation), and sign persona files in a local agents directory",
		Args:  cobra.RangeArgs(1, 3),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := a.localAgentsDir()
			if dir == "" {
				return exitWith(ExitUsage, errors.New("persona commands need a real local agents directory (run from a source checkout or pass --agents-dir); personas are embedded and hidden otherwise"))
			}
			noSig, _ := cmd.Flags().GetBool("no-signature")
			switch args[0] {
			case "show":
				if len(args) < 2 {
					return exitWith(ExitUsage, errors.New("usage: water persona show <role> [file]"))
				}
				return a.personaShow(dir, args[1], fileArg(args))
			case "edit":
				if len(args) < 3 {
					return exitWith(ExitUsage, errors.New("usage: water persona edit <role> <soul.md|experience.md|reasoning.md|skills/<slug>/SKILL.md>"))
				}
				return a.personaEdit(dir, args[1], args[2])
			case "sign":
				return a.personaSign(dir, noSig)
			case "verify":
				return a.personaVerify(dir)
			}
			return exitWith(ExitUsage, fmt.Errorf("unknown persona action %q", args[0]))
		},
	}
	c.Flags().Bool("no-signature", false, "sign: stamp role_id/content_hash only (portable across machines)")
	return c
}

func fileArg(args []string) string {
	if len(args) >= 3 {
		return args[2]
	}
	return ""
}

func (a *App) roleFiles(dir, role string) (string, []string, string, error) {
	roleDir := filepath.Join(dir, filepath.Base(role))
	if _, err := os.Stat(filepath.Join(roleDir, "role.yaml")); err != nil {
		return "", nil, "", exitWith(ExitUsage, fmt.Errorf("no role %q under %s", role, dir))
	}
	files, err := identity.PersonaFiles(roleDir)
	if err != nil {
		return "", nil, "", err
	}
	rid, err := identity.RoleIDFromDir(roleDir)
	return roleDir, files, rid, err
}

func (a *App) personaShow(dir, role, file string) error {
	roleDir, files, rid, err := a.roleFiles(dir, role)
	if err != nil {
		return err
	}
	var key []byte
	if k := a.keys(); k != nil {
		key = k.Key
	}
	if file != "" {
		p := filepath.Join(roleDir, file)
		b, err := os.ReadFile(p)
		if err != nil {
			return exitWith(ExitUsage, err)
		}
		st := identity.Status(file, b, rid, key, key != nil)
		fmt.Fprintf(os.Stderr, "%s %s  %s\n", surface.StyleDim.Render("identity"), st.Verified, surface.StyleDim.Render(st.Hash))
		fmt.Print(string(b))
		return nil
	}
	if a.jsonMode() {
		var out []identity.FileStatus
		for _, f := range files {
			b, _ := os.ReadFile(f)
			rel, _ := filepath.Rel(roleDir, f)
			out = append(out, identity.Status(rel, b, rid, key, key != nil))
		}
		return printJSON(map[string]any{"role": role, "role_id": rid, "files": out})
	}
	fmt.Printf("%s %s  %s %s\n", surface.StyleDim.Render("role"), surface.StyleBold.Render(role), surface.StyleDim.Render("role_id"), rid)
	for _, f := range files {
		b, _ := os.ReadFile(f)
		rel, _ := filepath.Rel(roleDir, f)
		st := identity.Status(rel, b, rid, key, key != nil)
		mark := surface.StyleOK.Render("✓")
		if st.Verified != "ok" {
			mark = surface.StyleWarn.Render("!")
		}
		if strings.Contains(st.Verified, "failed") {
			mark = surface.StyleErr.Render("×")
		}
		fmt.Printf("  %s %-36s %s\n", mark, rel, surface.StyleDim.Render(st.Verified))
	}
	return nil
}

// personaEdit stages the file to a 0600 temp copy, opens $EDITOR, diffs on
// exit, writes only on explicit confirmation, re-stamps hash and signature,
// and commits to the git-backed persona journal.
func (a *App) personaEdit(dir, role, file string) error {
	roleDir, _, rid, err := a.roleFiles(dir, role)
	if err != nil {
		return err
	}
	if rid == "" {
		return exitWith(ExitUsage, fmt.Errorf("role %s has no role_id; run `water persona sign` first", role))
	}
	p := filepath.Join(roleDir, filepath.Clean(file))
	if !strings.HasPrefix(p, roleDir+string(filepath.Separator)) {
		return exitWith(ExitUsage, errors.New("file must be inside the role directory"))
	}
	if identity.FileType(p) == "" {
		return exitWith(ExitUsage, fmt.Errorf("%s is not a persona file (soul.md, experience.md, reasoning.md, skills/*/SKILL.md)", file))
	}
	orig, err := os.ReadFile(p)
	if err != nil {
		return exitWith(ExitUsage, err)
	}
	ed := os.Getenv("VISUAL")
	if ed == "" {
		ed = os.Getenv("EDITOR")
	}
	if ed == "" {
		return exitWith(ExitUsage, errors.New("$EDITOR is not set"))
	}
	tmp, err := os.CreateTemp("", "water-persona-*"+filepath.Ext(p))
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_ = os.Chmod(tmp.Name(), 0o600)
	_, _ = tmp.Write(orig)
	tmp.Close()
	parts := strings.Fields(ed)
	cmd := exec.Command(parts[0], append(parts[1:], tmp.Name())...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor: %w", err)
	}
	edited, err := os.ReadFile(tmp.Name())
	if err != nil {
		return err
	}
	if string(edited) == string(orig) {
		fmt.Fprintln(os.Stderr, "no changes")
		return nil
	}
	// Show the diff (git diff --no-index when available; else a summary).
	if git, err := exec.LookPath("git"); err == nil {
		d := exec.Command(git, "--no-pager", "diff", "--no-index", "--", p, tmp.Name())
		d.Stdout, d.Stderr = os.Stderr, os.Stderr
		_ = d.Run()
	} else {
		fmt.Fprintf(os.Stderr, "%d bytes → %d bytes\n", len(orig), len(edited))
	}
	if !a.flags.yes {
		fmt.Fprintf(os.Stderr, "write %s and re-sign? [y/N] ", file)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if ans := strings.ToLower(strings.TrimSpace(line)); ans != "y" && ans != "yes" {
			fmt.Fprintln(os.Stderr, "declined; file untouched")
			return nil
		}
	}
	kr, created, err := identity.EnsureKeyring(config.Home())
	if err != nil {
		return err
	}
	if created {
		// First keyring on this machine: adopt every file so nothing else
		// becomes "unsigned" as a side effect of this edit.
		if _, err := identity.StampDir(dir, kr.Key); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "created", kr.Path, "and signed all persona files")
	}
	stamped, err := identity.Stamp(edited, rid, identity.FileType(p), kr.Key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(p, stamped, 0o644); err != nil {
		return err
	}
	rel, _ := filepath.Rel(roleDir, p)
	j := identity.Journal{Dir: filepath.Join(config.Home(), "persona-journal")}
	if err := j.Record(role, rel, stamped, fmt.Sprintf("%s: edit %s", role, rel)); err != nil {
		fmt.Fprintln(os.Stderr, "journal:", err)
	}
	fmt.Fprintln(os.Stderr, "wrote", p, "(re-stamped and signed; journaled)")
	return nil
}

func (a *App) personaSign(dir string, noSig bool) error {
	var key []byte
	if !noSig {
		kr, created, err := identity.EnsureKeyring(config.Home())
		if err != nil {
			return err
		}
		if created {
			fmt.Fprintln(os.Stderr, "created", kr.Path)
		}
		key = kr.Key
	}
	touched, err := identity.StampDir(dir, key)
	if err != nil {
		return err
	}
	for _, f := range touched {
		fmt.Println(f)
	}
	fmt.Fprintf(os.Stderr, "stamped %d file(s)\n", len(touched))
	return nil
}

func (a *App) personaVerify(dir string) error {
	a.flags.agentsDir = dir
	a.src, a.roles = nil, nil
	reg, err := a.roleRegistry()
	if err != nil {
		return exitWith(ExitError, err)
	}
	fmt.Fprintf(os.Stderr, "%d role(s) verified from %s\n", len(reg.All()), dir)
	return nil
}
