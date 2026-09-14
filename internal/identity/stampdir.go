package identity

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// PersonaFiles lists the identity-bound files of a role directory on disk.
func PersonaFiles(roleDir string) ([]string, error) {
	var out []string
	for _, n := range []string{"soul.md", "experience.md"} {
		p := filepath.Join(roleDir, n)
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	skills, _ := os.ReadDir(filepath.Join(roleDir, "skills"))
	for _, e := range skills {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(roleDir, "skills", e.Name(), "SKILL.md")
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out, nil
}

// FileType maps a persona file path to its file_type value.
func FileType(path string) string {
	return FileTypes[filepath.Base(path)]
}

// RoleIDFromDir reads role_id from <roleDir>/role.yaml ("" if unset).
func RoleIDFromDir(roleDir string) (string, error) {
	b, err := os.ReadFile(filepath.Join(roleDir, "role.yaml"))
	if err != nil {
		return "", err
	}
	var m struct {
		RoleID string `yaml:"role_id"`
	}
	if err := yaml.Unmarshal(b, &m); err != nil {
		return "", err
	}
	return m.RoleID, nil
}

// StampDir stamps (and, with key, signs) every persona file of every role
// under agentsDir. Roles whose role.yaml has no role_id get one minted and
// written back. Returns the files touched.
func StampDir(agentsDir string, key []byte) ([]string, error) {
	entries, err := os.ReadDir(agentsDir)
	if err != nil {
		return nil, err
	}
	var touched []string
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		roleDir := filepath.Join(agentsDir, e.Name())
		if _, err := os.Stat(filepath.Join(roleDir, "role.yaml")); err != nil {
			continue
		}
		rid, err := RoleIDFromDir(roleDir)
		if err != nil {
			return touched, fmt.Errorf("%s: %w", roleDir, err)
		}
		if rid == "" {
			rid = NewRoleID()
			if err := writeRoleID(filepath.Join(roleDir, "role.yaml"), rid); err != nil {
				return touched, err
			}
			touched = append(touched, filepath.Join(roleDir, "role.yaml"))
		}
		files, _ := PersonaFiles(roleDir)
		for _, f := range files {
			raw, err := os.ReadFile(f)
			if err != nil {
				return touched, err
			}
			out, err := Stamp(raw, rid, FileType(f), key)
			if err != nil {
				return touched, fmt.Errorf("%s: %w", f, err)
			}
			if string(out) != string(raw) {
				if err := os.WriteFile(f, out, 0o644); err != nil {
					return touched, err
				}
				touched = append(touched, f)
			}
		}
	}
	return touched, nil
}

// writeRoleID inserts a role_id line after the slug line without reformatting
// the rest of role.yaml.
func writeRoleID(path, rid string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(b), "\n")
	var out []string
	done := false
	for _, ln := range lines {
		out = append(out, ln)
		if !done && strings.HasPrefix(strings.TrimSpace(ln), "slug:") {
			out = append(out, fmt.Sprintf("role_id: %q", rid))
			done = true
		}
	}
	if !done {
		out = append([]string{fmt.Sprintf("role_id: %q", rid)}, out...)
	}
	return os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o644)
}

// Journal is the git-backed persona journal: every confirmed edit is copied
// into <dir>/<role>/<file> and committed, so persona history survives outside
// the working tree. Falls back to a JSONL log when git is unavailable.
type Journal struct {
	Dir string
}

// Record copies file into the journal and commits it.
func (j Journal) Record(role, file string, content []byte, message string) error {
	if j.Dir == "" {
		return errors.New("journal dir is empty")
	}
	dest := filepath.Join(j.Dir, filepath.Base(role), strings.TrimPrefix(file, string(filepath.Separator)))
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(dest, content, 0o600); err != nil {
		return err
	}
	git, err := exec.LookPath("git")
	if err != nil {
		return j.appendLog(role, file, message)
	}
	if _, err := os.Stat(filepath.Join(j.Dir, ".git")); err != nil {
		if out, err := exec.Command(git, "-C", j.Dir, "init", "-q").CombinedOutput(); err != nil {
			return fmt.Errorf("git init: %s", strings.TrimSpace(string(out)))
		}
	}
	rel, _ := filepath.Rel(j.Dir, dest)
	if out, err := exec.Command(git, "-C", j.Dir, "add", "--", rel).CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %s", strings.TrimSpace(string(out)))
	}
	cmd := exec.Command(git, "-C", j.Dir, "-c", "user.name=water", "-c", "user.email=water@localhost", "commit", "-q", "-m", message, "--", rel)
	if out, err := cmd.CombinedOutput(); err != nil && !strings.Contains(string(out), "nothing to commit") {
		return fmt.Errorf("git commit: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

func (j Journal) appendLog(role, file, message string) error {
	f, err := os.OpenFile(filepath.Join(j.Dir, "journal.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "%s\t%s\t%s\t%s\n", time.Now().UTC().Format(time.RFC3339), role, file, message)
	return err
}
