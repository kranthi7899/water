package decisions

import (
	"context"
	"strings"
	"testing"
	"time"

	"water/internal/gate"
)

func TestExtractDeadline(t *testing.T) {
	loc := time.UTC
	// Wednesday 2026-09-23 10:00.
	recv := time.Date(2026, 9, 23, 10, 0, 0, 0, loc)
	at := func(y int, m time.Month, d, h, min int) *time.Time {
		v := time.Date(y, m, d, h, min, 0, 0, loc)
		return &v
	}
	cases := map[string]struct {
		text string
		recv time.Time
		want *time.Time
	}{
		"weekday and time":      {"Need this approved by Friday 5pm", recv, at(2026, 9, 25, 17, 0)},
		"weekday only is EOD":   {"please sign off by Monday", recv, at(2026, 9, 28, 17, 0)},
		"due 24h time":          {"Invoice due Thursday at 09:30.", recv, at(2026, 9, 24, 9, 30)},
		"tomorrow":              {"can you confirm by tomorrow?", recv, at(2026, 9, 24, 17, 0)},
		"eod":                   {"Need an answer by EOD", recv, at(2026, 9, 23, 17, 0)},
		"end of day":            {"reply before end of day please", recv, at(2026, 9, 23, 17, 0)},
		"iso date":              {"Deadline: 2026-10-01", time.Time{}, at(2026, 10, 1, 17, 0)},
		"month day":             {"no later than October 3rd at 2:30 pm", recv, at(2026, 10, 3, 14, 30)},
		"month day next year":   {"due by Jan 5", recv, at(2027, 1, 5, 17, 0)},
		"month day with year":   {"due by March 2, 2027", time.Time{}, at(2027, 3, 2, 17, 0)},
		"same deadline twice":   {"by Friday 5pm. Again: by Friday 5pm", recv, at(2026, 9, 25, 17, 0)},
		"no cue word":           {"Friday works for lunch", recv, nil},
		"nothing at all":        {"Can you approve this?", recv, nil},
		"two different":         {"by Friday, or at the latest by Monday", recv, nil},
		"next weekday is vague": {"by next Friday", recv, nil},
		"same weekday is vague": {"by Wednesday", recv, nil},
		"relative without time": {"by Friday", time.Time{}, nil},
		"impossible date":       {"due by February 30", recv, nil},
		"bad hour":              {"by Friday 13pm", recv, nil},
		"year alone":            {"by 2027", recv, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := extractDeadline(tc.text, tc.recv, loc)
			switch {
			case tc.want == nil && got != nil:
				t.Fatalf("%q: guessed %v", tc.text, got)
			case tc.want != nil && (got == nil || !got.Equal(*tc.want)):
				t.Fatalf("%q: got %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// TestBuildExtractsTheDeadline: a built card carries the item's deadline
// end to end, so Rank and Speak use it.
func TestBuildExtractsTheDeadline(t *testing.T) {
	r := registry(t, map[string]string{"budget.yaml": budgetYAML})
	g := &fakeGate{answers: map[string]func(gate.Call) (gate.Result, error){"gmail.list_messages": none}}
	sent := time.Date(2026, 9, 23, 10, 0, 0, 0, time.Local)
	due := msg("m1", true, "dana@x.com", "Laptop budget")
	due.Body, due.SentAt = "Need this approved by Friday 5pm.", sent
	undated := msg("m2", true, "dana@x.com", "Another budget")
	b := &Builder{Registry: r, Gate: g}
	c1, err := b.Build(context.Background(), due, Classification{TypeID: "budget"})
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 9, 25, 17, 0, 0, 0, time.Local)
	if c1.Deadline == nil || !c1.Deadline.Equal(want) {
		t.Fatalf("deadline: %v, want %v", c1.Deadline, want)
	}
	if strings.Contains(strings.Join(c1.Gaps, "|"), "deadline") {
		t.Fatalf("a found deadline is not a gap: %q", c1.Gaps)
	}
	if !strings.Contains(c1.Speak(), "Due Friday, September 25") {
		t.Fatalf("speak: %q", c1.Speak())
	}
	c2, _ := b.Build(context.Background(), undated, Classification{TypeID: "budget"})
	if c2.Deadline != nil || !strings.Contains(strings.Join(c2.Gaps, "|"), "No deadline could be extracted from the item.") {
		t.Fatalf("undated: %v %q", c2.Deadline, c2.Gaps)
	}
	if got := Rank([]*Card{c2, c1}); got[0] != c1 {
		t.Fatal("the card with a deadline must rank first at equal severity")
	}
}
