package chat

import (
	"fmt"
	"path/filepath"
	"strings"

	"water/internal/tools"
)

// operationSummary renders the Part 9 muted line for a turn: what mechanical
// action it actually took, read straight from the trace events the backend
// returned for it (the same data /why and diagnose already read). It is
// never a hand-written description independent of that data — an empty
// ToolEvents means an empty summary, and the counts and path always match
// what's in the events.
// DenialLines renders refused tool calls as "tool path — reason" lines.
func DenialLines(evs []tools.Event) []string {
	var out []string
	for _, e := range evs {
		if e.Allowed {
			continue
		}
		target := ""
		if p, ok := e.Args["path"].(string); ok {
			target = " " + p
		} else if c, ok := e.Args["command"].(string); ok {
			target = " " + c
		}
		out = append(out, fmt.Sprintf("%s%s — %s", e.Tool, target, e.Basis))
	}
	return out
}

func operationSummary(t Turn) string {
	if len(t.ToolEvents) == 0 {
		return ""
	}
	var reads, lists, writes, runs, opens int
	var dirs []string
	for _, ev := range t.ToolEvents {
		if !ev.Allowed {
			continue
		}
		if p, ok := ev.Args["path"].(string); ok && p != "" {
			if ev.Tool == tools.ToolListDir {
				dirs = append(dirs, p) // list_dir's path IS the directory
			} else {
				dirs = append(dirs, filepath.Dir(p))
			}
		}
		switch ev.Tool {
		case tools.ToolReadFile:
			reads++
		case tools.ToolListDir:
			lists++
		case tools.ToolWriteFile:
			writes++
		case tools.ToolRun:
			runs++
		case tools.ToolOpenPage:
			opens++
		}
	}
	var parts []string
	if reads > 0 {
		parts = append(parts, fmt.Sprintf("read %d file%s", reads, plural(reads, "", "s")))
	}
	if lists > 0 {
		parts = append(parts, fmt.Sprintf("listed %d director%s", lists, plural(lists, "y", "ies")))
	}
	if writes > 0 {
		parts = append(parts, fmt.Sprintf("wrote %d file%s", writes, plural(writes, "", "s")))
	}
	if runs > 0 {
		parts = append(parts, fmt.Sprintf("ran %d shell command%s", runs, plural(runs, "", "s")))
	}
	if opens > 0 {
		parts = append(parts, fmt.Sprintf("opened %d page%s in the browser", opens, plural(opens, "", "s")))
	}
	if len(parts) == 0 {
		return ""
	}
	summary := strings.Join(parts, ", ")
	if root := commonDir(dirs); root != "" {
		summary += " under " + root
	}
	return summary
}

func plural(n int, singular, pluralSuffix string) string {
	if n == 1 {
		return singular
	}
	return pluralSuffix
}

// commonDir returns the deepest directory shared by every already-resolved
// directory in dirs, or "" when there's no data or no ancestor worth naming.
func commonDir(dirs []string) string {
	if len(dirs) == 0 {
		return ""
	}
	common := dirs[0]
	for _, d := range dirs[1:] {
		for common != "." && common != "/" && !strings.HasPrefix(d+"/", common+"/") {
			common = filepath.Dir(common)
		}
	}
	if common == "." || common == "/" {
		return ""
	}
	return common
}
