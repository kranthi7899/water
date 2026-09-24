package runtime

import (
	"context"
	"fmt"
	"strings"
	"time"

	"water/internal/approvals"
	"water/internal/store"
)

// FastPath answers a small, conservative set of questions straight from the
// store, with no model call. Anything it does not clearly match falls
// through (ok=false) to the model — including near-misses that merely
// contain a trigger word inside a different kind of sentence (e.g. "help me
// schedule a meeting with the calendar team" is a request to create an
// event, not a question about today's calendar, and must fall through).
func FastPath(ctx context.Context, env Env, prompt string) (string, bool) {
	p := norm(prompt)
	if day, ok := matchSchedule(p); ok {
		return scheduleAnswer(ctx, env, day), true
	}
	if matchApprovals(p) {
		return approvalsAnswer(ctx, env), true
	}
	if matchBrief(p) {
		return "Your morning brief isn't ready yet.", true
	}
	return "", false
}

// norm lowercases, collapses whitespace, and strips the small set of
// punctuation that would otherwise break a phrase match ("what's" stays
// intact; a trailing "?" or "." does not).
func norm(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Map(func(r rune) rune {
		switch r {
		case '?', '.', '!', ',':
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

func containsAny(p string, phrases ...string) bool {
	for _, ph := range phrases {
		if strings.Contains(p, ph) {
			return true
		}
	}
	return false
}

// matchSchedule recognises a question about today's or tomorrow's calendar.
// It requires an explicit possessive/question phrase ("my calendar", "any
// meetings", ...), not just a bare keyword, so a request to CREATE an event
// or meeting never matches.
func matchSchedule(p string) (day string, ok bool) {
	triggers := []string{
		"my schedule", "my calendar", "my agenda", "my meetings",
		"on my calendar", "any meetings", "meetings do i have",
		"what's on my", "whats on my", "what is on my",
	}
	if !containsAny(p, triggers...) {
		return "", false
	}
	tomorrow := containsAny(p, "tomorrow")
	today := containsAny(p, "today")
	switch {
	case tomorrow && !today:
		return "tomorrow", true
	default:
		return "today", true
	}
}

// matchApprovals recognises a question about the approval queue.
func matchApprovals(p string) bool {
	triggers := []string{
		"pending approval", "pending approvals", "any approvals",
		"approvals waiting", "waiting for my approval", "waiting on my approval",
		"what needs my approval", "what's pending", "whats pending",
	}
	return containsAny(p, triggers...)
}

// matchBrief recognises a question about the (not yet built, A4) morning
// brief.
func matchBrief(p string) bool {
	triggers := []string{"my brief", "morning brief", "today's brief", "todays brief", "the brief ready"}
	return containsAny(p, triggers...)
}

func scheduleAnswer(ctx context.Context, env Env, day string) string {
	if env.Store == nil {
		return "I don't have a calendar connected yet."
	}
	now := env.now()
	start := startOfDay(now)
	if day == "tomorrow" {
		start = start.Add(24 * time.Hour)
	}
	end := start.Add(24 * time.Hour)
	todays, err := store.EventsInRange(ctx, env.Store, start, end)
	if err != nil {
		return "I couldn't read the calendar just now."
	}
	if len(todays) == 0 {
		return fmt.Sprintf("Nothing on the calendar for %s.", day)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d event(s).\n", strings.ToUpper(day[:1])+day[1:], len(todays))
	for _, e := range todays {
		fmt.Fprintf(&b, "- %s %s\n", e.StartAt.Local().Format("15:04"), e.Title)
	}
	return strings.TrimSpace(b.String())
}

func approvalsAnswer(ctx context.Context, env Env) string {
	if env.Approvals == nil {
		return "No approval queue is attached."
	}
	envs, err := env.Approvals.Pending(ctx)
	if err != nil {
		return "I couldn't read the approval queue just now."
	}
	return approvals.Menu(envs)
}
