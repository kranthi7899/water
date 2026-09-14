package chat

import (
	"strings"
	"testing"

	themepkg "water/internal/theme"
	"water/internal/tools"
)

// fixtureConversation is the multi-turn conversation the Part 9 tests render:
// two user turns and two agent turns, one of which made a real tool call.
func fixtureConversation(m *model) (userLines, replyLines [][]string) {
	u1 := m.renderUser("Should we adopt a new vector database the vendor says is 10x faster?")
	r1 := m.renderReply(Turn{
		Reply: "I have no grounds to validate that claim without an independent benchmark.",
		ToolEvents: []tools.Event{
			{Tool: tools.ToolReadFile, Allowed: true, Args: map[string]any{"path": "/work/board-materials/vendor-benchmark.pdf"}},
		},
	})
	u2 := m.renderUser("Fine, what would you need to see?")
	r2 := m.renderReply(Turn{Reply: "A reproducible benchmark on our own traffic shape, not the vendor's."})
	return [][]string{u1, u2}, [][]string{r1, r2}
}

func TestUserTintNeverOnAgentTurn(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 100, 30
	if err := m.enterRole("ceo", ""); err != nil {
		t.Fatal(err)
	}
	m.styler.Profile = themepkg.TrueColor
	m.relayout()

	userLines, replyLines := fixtureConversation(m)

	for ti, turn := range userLines {
		for li, line := range turn {
			if strings.TrimSpace(line) == "" {
				continue // the trailing separator carries no text either way
			}
			if !strings.Contains(line, "\x1b[48;") {
				t.Errorf("user turn %d line %d missing background tint: %q", ti, li, line)
			}
		}
	}
	for ti, turn := range replyLines {
		for li, line := range turn {
			if strings.Contains(line, "\x1b[48;") {
				t.Errorf("agent turn %d line %d carries a background tint (forbidden): %q", ti, li, line)
			}
		}
	}
}

func TestOperationLineSourcedFromTrace(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 100, 30
	if err := m.enterRole("ceo", ""); err != nil {
		t.Fatal(err)
	}
	m.relayout()

	cases := []struct {
		name   string
		events []tools.Event
		want   string
	}{
		{
			name: "single read under a directory",
			events: []tools.Event{
				{Tool: tools.ToolReadFile, Allowed: true, Args: map[string]any{"path": "/work/board-materials/config.yaml"}},
			},
			want: "read 1 file under /work/board-materials",
		},
		{
			name: "two reads with no common directory",
			events: []tools.Event{
				{Tool: tools.ToolReadFile, Allowed: true, Args: map[string]any{"path": "/a/one.txt"}},
				{Tool: tools.ToolReadFile, Allowed: true, Args: map[string]any{"path": "/b/two.txt"}},
			},
			want: "read 2 files",
		},
		{
			name: "read plus a directory listing",
			events: []tools.Event{
				{Tool: tools.ToolReadFile, Allowed: true, Args: map[string]any{"path": "/work/config.yaml"}},
				{Tool: tools.ToolListDir, Allowed: true, Args: map[string]any{"path": "/work"}},
			},
			want: "read 1 file, listed 1 directory under /work",
		},
		{
			name: "a denied call is not counted",
			events: []tools.Event{
				{Tool: tools.ToolReadFile, Allowed: false, Args: map[string]any{"path": "/secret/config.yaml"}},
			},
			want: "",
		},
		{
			name:   "no tool calls at all",
			events: nil,
			want:   "",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			turn := Turn{Reply: "the answer", ToolEvents: c.events}
			if got := operationSummary(turn); got != c.want {
				t.Fatalf("operationSummary() = %q, want %q", got, c.want)
			}
			lines := m.renderReply(turn)
			if c.want == "" {
				for _, l := range lines {
					if strings.Contains(l, "·") {
						t.Fatalf("no operation line expected, found one: %q", l)
					}
				}
				return
			}
			if len(lines) < 2 || !strings.Contains(lines[1], "· "+c.want) {
				t.Fatalf("operation-summary line not rendered from trace data: %#v", lines)
			}
		})
	}
}

func TestDegradesWithoutTint(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 100, 30
	if err := m.enterRole("ceo", ""); err != nil {
		t.Fatal(err)
	}
	m.relayout()

	tiers := []themepkg.Profile{themepkg.None, themepkg.ANSI16, themepkg.ANSI256, themepkg.TrueColor}
	for _, tier := range tiers {
		m.styler.Profile = tier
		lines := m.renderUser("we're thinking about shipping this before Black Friday")
		joined := strings.Join(lines, "\n")
		if !strings.Contains(joined, userPrefix) {
			t.Errorf("profile %s: prefix %q missing from user turn: %q", tier, userPrefix, joined)
		}
		hasTint := strings.Contains(joined, "\x1b[48;")
		wantTint := tier == themepkg.TrueColor
		if hasTint != wantTint {
			t.Errorf("profile %s: background tint present=%v, want %v", tier, hasTint, wantTint)
		}
	}
}
