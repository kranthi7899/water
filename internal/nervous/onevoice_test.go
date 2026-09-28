package nervous

import (
	"strings"
	"sync"
	"testing"
	"time"

	"water/internal/backend"
	"water/internal/nervous/intents"
	"water/internal/nervous/render"
	"water/internal/nervous/turn"
	"water/internal/runtime"
	"water/internal/store"
)

// TestOneVoiceAcrossTiers proves the one-voice contract (Design §8.4): the
// same underlying fact ("board sync at 3pm tomorrow"), delivered once
// through a Tier 0 answer and once through a fake main-path answer that
// returns markdown plus a banned phrase, produces speakable voice output in
// both cases — no markdown, within the style's voice length cap — and the
// banned phrase surfaces only as a lint warning on the main-path case, never
// silently rewritten out of the delivered text.
func TestOneVoiceAcrossTiers(t *testing.T) {
	style := render.DefaultStyle()

	// --- Tier 0 side: schedule.on_date answers "board sync" tomorrow at 3pm.
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	env, ctx, fk := nervousTestEnv(t)
	tomorrow3pm := time.Date(2026, 9, 25, 15, 0, 0, 0, tier0FixedNow.Location())
	if err := env.Store.Upsert(ctx, &store.Event{
		Meta:    store.Meta{Source: "gcal", SourceID: "e1", CreatedAt: tier0FixedNow},
		Title:   "Board sync",
		StartAt: tomorrow3pm,
		EndAt:   tomorrow3pm.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	table := turn.NewTable(func() time.Time { return tier0FixedNow })
	n, err := New(Config{
		Registry: func() *intents.Registry { return reg }, Style: style,
		Turns: table, Tier0Enabled: true, MainEnabled: true, Clock: realClock{}, AckAfter: DefaultAckAfter,
	})
	if err != nil {
		t.Fatal(err)
	}

	var quickEvents []runtime.Event
	var mu sync.Mutex
	n.Handle(ctx, env, Turn{Channel: runtime.ChannelVoice, Text: "what's on my calendar tomorrow", TaskID: "voice-t0"}, collect(&quickEvents, &mu))
	if fk.Calls() != 0 {
		t.Fatalf("Tier 0 side made %d backend calls, want 0", fk.Calls())
	}
	quickSpoken := joinSentences(quickEvents)
	if quickSpoken == "" {
		t.Fatal("Tier 0 side produced no spoken sentences")
	}
	assertSpeakable(t, "quick", quickSpoken, style.MaxChars("voice"))

	// --- Main-path side: the model returns markdown plus a banned phrase.
	env2, ctx2, fk2 := nervousTestEnv(t)
	fk2.Reply = func(req backend.Request) string {
		return "# Board sync\nAs an AI, I can tell you it's **tomorrow at 3pm**."
	}
	reg2 := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})

	var lintWarnings []string
	table2 := turn.NewTable(func() time.Time { return tier0FixedNow })
	n2, err := New(Config{
		Registry: func() *intents.Registry { return reg2 }, Style: style,
		Turns: table2, Tier0Enabled: true, MainEnabled: true, Clock: realClock{}, AckAfter: DefaultAckAfter,
		OnVoiceLint: func(w []string) { lintWarnings = append(lintWarnings, w...) },
	})
	if err != nil {
		t.Fatal(err)
	}

	var mainEvents []runtime.Event
	// "should" is an escalate word, forcing this straight to the main path
	// without ever trying Tier 0.
	n2.Handle(ctx2, env2, Turn{Channel: runtime.ChannelVoice, Text: "what should i say about the board sync", TaskID: "voice-main"}, collect(&mainEvents, &mu))
	if fk2.Calls() != 1 {
		t.Fatalf("main-path side made %d backend calls, want 1", fk2.Calls())
	}
	mainSpoken := joinSentences(mainEvents)
	if mainSpoken == "" {
		t.Fatal("main-path side produced no spoken sentences")
	}
	assertSpeakable(t, "main", mainSpoken, style.MaxChars("voice"))

	// Lint never rewrites prose (Design §8.3/§8.4): the banned phrase must
	// still be exactly present in the delivered speech, not silently
	// stripped or replaced — the ONLY thing that changes is that it's also
	// recorded as a lint warning, checked below.
	if !strings.Contains(strings.ToLower(mainSpoken), "as an ai") {
		t.Fatalf("main-path delivered text lost the banned phrase; Lint must never rewrite prose: %q", mainSpoken)
	}
	found := false
	for _, w := range lintWarnings {
		if w == "banned:as an ai" {
			found = true
		}
	}
	if !found {
		t.Fatalf("lint warnings = %v, want a banned:as an ai warning surfaced from the main-path reply", lintWarnings)
	}
}

func joinSentences(events []runtime.Event) string {
	var sb strings.Builder
	for _, e := range events {
		if e.Kind == runtime.EventSentence {
			sb.WriteString(e.Text)
			sb.WriteString(" ")
		}
	}
	return strings.TrimSpace(sb.String())
}

func assertSpeakable(t *testing.T, label, text string, maxChars int) {
	t.Helper()
	for _, bad := range []string{"#", "**", "```", "|---"} {
		if strings.Contains(text, bad) {
			t.Errorf("%s side: spoken text still contains markdown %q: %q", label, bad, text)
		}
	}
	if maxChars > 0 && len(text) > maxChars {
		// The quick tier's own output is hard-capped; the main path is only
		// linted (Design §8.4), so allow up to 1.5x here and let the
		// dedicated lint test cover the exact threshold.
		if float64(len(text)) > float64(maxChars)*1.5 {
			t.Errorf("%s side: spoken text length %d far exceeds the voice cap %d: %q", label, len(text), maxChars, text)
		}
	}
}
