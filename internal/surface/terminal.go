package surface

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"water/internal/backend"
	"water/internal/orchestrator"
)

func init() {
	Register("terminal", func(o Options) Surface { return NewTerminal(os.Stderr, os.Stdout, o) })
}

// Terminal renders progress to stderr and the final output to stdout, so
// `water orchestrate ... > out.md` captures only the synthesis.
type Terminal struct {
	mu   sync.Mutex
	prog io.Writer
	out  io.Writer
	opts Options
	t0   time.Time
}

// NewTerminal builds a terminal surface writing progress to prog, results to out.
func NewTerminal(prog, out io.Writer, o Options) *Terminal {
	return &Terminal{prog: prog, out: out, opts: o}
}

func (t *Terminal) Name() string { return "terminal" }

func (t *Terminal) pf(format string, a ...any) {
	if t.opts.Quiet {
		return
	}
	fmt.Fprintf(t.prog, format, a...)
}

func (t *Terminal) RunStarted(runID, brief string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.t0 = time.Now()
	t.pf("%s\n", Rule())
	t.pf("%s %s\n", StyleDim.Render("run"), StyleAccent.Render(runID))
	if brief != "" {
		t.pf("%s %s\n", StyleDim.Render("brief"), StyleInk.Render(truncate(brief, 100)))
	}
	t.pf("%s\n", Rule())
}

func (t *Terminal) NodeStarted(role string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pf("  %s %s\n", StyleDim.Render("◦"), StyleBold.Render(role))
}

func (t *Terminal) NodeFinished(role string, r backend.Response) {
	t.mu.Lock()
	defer t.mu.Unlock()
	flag := ""
	if r.Metered {
		flag = " " + StyleWarn.Render("METERED")
	}
	t.pf("  %s %s %s%s\n", StyleAccent.Render("•"), StyleBold.Render(role),
		StyleDim.Render(fmt.Sprintf("%s %s", r.Backend, r.Duration.Round(time.Millisecond))), flag)
	if t.opts.Verbose && r.Text != "" {
		for _, line := range strings.Split(strings.TrimSpace(r.Text), "\n") {
			t.pf("      %s\n", StyleDim.Render(line))
		}
	}
}

func (t *Terminal) NodeFailed(role string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pf("  %s %s %s\n", StyleErr.Render("×"), StyleBold.Render(role), StyleDim.Render(err.Error()))
}

func (t *Terminal) MessageSent(m orchestrator.AgentMessage) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pf("    %s %s %s %s %s\n", StyleDim.Render("→"), m.From, StyleDim.Render("›"), m.To, StyleDim.Render("["+m.Topic+"]"))
}

func (t *Terminal) RunFinished(final string, s RunStats) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if final != "" {
		if !t.opts.Quiet {
			fmt.Fprintf(t.prog, "%s\n", Rule())
		}
		fmt.Fprintln(t.out, strings.TrimSpace(final))
	}
	t.pf("%s\n", Rule())
	metered := StyleOK.Render(fmt.Sprintf("%d", s.MeteredCalls))
	if s.MeteredCalls > 0 {
		metered = StyleWarn.Render(fmt.Sprintf("%d  ← metered money was spent", s.MeteredCalls))
	}
	t.pf("%s  backend %s  calls %d  metered %s  wall %s\n",
		StyleDim.Render("summary"), StyleAccent.Render(strings.Join(s.Backends, ",")), s.Calls, metered, s.Wall.Round(time.Millisecond))
	for _, rt := range s.Roles {
		t.pf("         %-10s %d call(s)  %s\n", rt.Role, rt.Calls, rt.Duration.Round(time.Millisecond))
	}
	if s.TracePath != "" {
		t.pf("%s %s\n", StyleDim.Render("trace"), StyleDim.Render(s.TracePath))
	}
}

func truncate(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
