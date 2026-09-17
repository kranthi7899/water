package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// A browser is the one thing an approved plan launches outside the command
// sandbox, and a browser can reach the network. A page is opened only when
// nothing in it or its stylesheets can load code or contact a host, so an
// agent-written page cannot carry workspace data anywhere.
var (
	pageHazard = regexp.MustCompile(`(?i)\b(?:https?|ftps?|wss?|file):\s*[/\\]` +
		`|[("'=\s,][/\\]{2}[^\s/\\*]` +
		`|[("'=\s]javascript\s*:` +
		`|<\s*(?:script|iframe|object|embed|frame)\b` +
		`|\bon[a-z]+\s*=` +
		`|@import`)
	stylesheetRef = regexp.MustCompile(`(?i)href\s*=\s*["']?([^"'\s>]+\.css)\b`)
)

const (
	maxPageFiles = 64
	maxPageBytes = 4 << 20
)

// launchPage hands a checked page to the platform's default browser. Tests
// replace it so no browser opens.
var launchPage = func(ctx context.Context, path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.CommandContext(ctx, "/usr/bin/open", path)
	case "linux":
		bin, err := exec.LookPath("xdg-open")
		if err != nil {
			return errors.New("xdg-open is not installed")
		}
		cmd = exec.CommandContext(ctx, bin, path)
	default:
		return fmt.Errorf("opening pages is not supported on %s", runtime.GOOS)
	}
	env := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=en_US.UTF-8"}
	for _, k := range []string{"HOME", "DISPLAY", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS"} {
		if v := os.Getenv(k); v != "" {
			env = append(env, k+"="+v)
		}
	}
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// openPage shows an existing, self-contained page. The path has already been
// resolved inside the workspace; the argv is fixed and no shell is involved.
func (s *Service) openPage(ctx context.Context, path string) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file", path)
	}
	if err := s.checkSelfContained(path); err != nil {
		return "", err
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := launchPage(cctx, path); err != nil {
		return "", fmt.Errorf("opening %s: %w", path, err)
	}
	return "opened " + path + " in the default browser", nil
}

// checkSelfContained scans the page, every page and stylesheet beside it, and
// any stylesheet it links elsewhere in the workspace.
func (s *Service) checkSelfContained(page string) error {
	dir := filepath.Dir(page)
	files := []string{page}
	seen := map[string]bool{page: true}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".html", ".htm", ".css":
			p := filepath.Join(dir, e.Name())
			if e.Type().IsRegular() && !seen[p] {
				seen[p] = true
				files = append(files, p)
			}
		}
	}
	total := 0
	for i := 0; i < len(files); i++ {
		if len(files) > maxPageFiles {
			return fmt.Errorf("%w: more than %d pages and stylesheets beside %s; put the site in its own folder", ErrDenied, maxPageFiles, filepath.Base(page))
		}
		b, err := os.ReadFile(files[i])
		if err != nil {
			if errors.Is(err, os.ErrNotExist) && i > 0 {
				continue
			}
			return err
		}
		if total += len(b); total > maxPageBytes {
			return fmt.Errorf("%w: site beside %s is over %d bytes", ErrDenied, filepath.Base(page), maxPageBytes)
		}
		if loc := pageHazard.FindIndex(b); loc != nil {
			return fmt.Errorf("%w: %s is not self-contained (found %q); open_page shows only pages with no scripts, embeds, event handlers, @import or remote URLs", ErrDenied, filepath.Base(files[i]), excerpt(b, loc))
		}
		if ext := strings.ToLower(filepath.Ext(files[i])); ext != ".html" && ext != ".htm" {
			continue
		}
		for _, m := range stylesheetRef.FindAllSubmatch(b, -1) {
			ref := filepath.Join(filepath.Dir(files[i]), string(m[1]))
			resolved, _, err := ResolveWithinRoots(s.Policy.Filesystem.Roots, ref)
			if err != nil {
				return fmt.Errorf("%w: stylesheet %s is outside the workspace", ErrDenied, m[1])
			}
			if !seen[resolved] {
				seen[resolved] = true
				files = append(files, resolved)
			}
		}
	}
	return nil
}

func excerpt(b []byte, loc []int) string {
	start, end := max(0, loc[0]-12), min(len(b), loc[1]+12)
	return strings.Join(strings.Fields(string(b[start:end])), " ")
}
