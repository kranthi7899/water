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
// A wrong fast-path answer silently drops the CEO's real request, so
// fastPathEligible rules out anything that asks for an action, names a time
// the fast path cannot answer for, or is long enough to be more than a
// status question, before any trigger is looked at.
func FastPath(ctx context.Context, env Env, prompt string) (string, bool) {
	p := norm(prompt)
	if !fastPathEligible(p) {
		return "", false
	}
	if day, ok := matchSchedule(p); ok {
		return scheduleAnswer(ctx, env, day), true
	}
	if matchApprovals(p) {
		return approvalsAnswer(ctx, env), true
	}
	if matchBrief(p) {
		return briefAnswer(ctx, env), true
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

// maxFastPathWords bounds a fast-path prompt: the status questions it
// answers are short, and anything longer is likely asking for more.
const maxFastPathWords = 10

// fastPathActionWords mark a request to do something (to an event, a
// message, an approval or the brief), which only the model can handle.
var fastPathActionWords = map[string]bool{
	"reschedule": true, "cancel": true, "move": true, "book": true, "draft": true, "reply": true,
	"send": true, "write": true, "rewrite": true, "summarize": true, "summarise": true, "approve": true,
	"deny": true, "reject": true, "decline": true, "forward": true, "delete": true, "remove": true,
	"create": true, "add": true, "invite": true, "prepare": true, "email": true, "update": true,
	"change": true, "edit": true, "accept": true, "push": true, "shift": true,
}

// fastPathOtherTimes mark a time scope the fast path cannot answer for (it
// knows only today and tomorrow).
var fastPathOtherTimes = map[string]bool{
	"yesterday": true, "week": true, "weekend": true, "month": true, "year": true, "next": true, "last": true,
	"monday": true, "tuesday": true, "wednesday": true, "thursday": true, "friday": true, "saturday": true, "sunday": true,
	"january": true, "february": true, "march": true, "april": true, "june": true, "july": true, "august": true,
	"september": true, "october": true, "november": true, "december": true,
}

// fastPathEligible is the gate in front of every trigger match (see
// FastPath): short, no action verb, no other time scope, no digits.
func fastPathEligible(p string) bool {
	words := strings.Fields(p)
	if len(words) == 0 || len(words) > maxFastPathWords {
		return false
	}
	for _, w := range words {
		w = strings.Trim(w, "'\"()")
		if fastPathActionWords[w] || fastPathOtherTimes[w] {
			return false
		}
		if strings.ContainsAny(w, "0123456789") {
			return false
		}
	}
	return true
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
// meetings", ...), not just a bare keyword; requests to create, move or
// cancel an event, and questions about any other day, were already ruled
// out by fastPathEligible.
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

// briefAnswer is the one deliberate exception to "the fast path never calls
// the model": it returns today's cached morning brief if one exists, and
// otherwise computes it once (ComputeAndCacheBrief's compute-once guard
// covers a background precompute racing an on-demand ask like this one) and
// caches it. Taint is checked every call, cache hit or not: the local
// signals are re-read, and the taint recorded with the cached brief (which
// covers its open cards) is added in. It fails closed — if the signals
// cannot be read, or the brief's recorded taint is unknown, the session is
// escalated, since the brief may carry external content either way.
func briefAnswer(ctx context.Context, env Env) string {
	if env.Store == nil {
		return "I don't have a store attached yet."
	}
	text, err := ComputeAndCacheBrief(ctx, env)
	if env.OnTaint != nil {
		_, tainted, serr := computeBriefSignals(ctx, env)
		if err == nil {
			tainted = tainted || cachedBriefTainted(ctx, env, startOfDay(env.now()).Format("2006-01-02"))
		}
		env.OnTaint(tainted || serr != nil)
	}
	if err != nil {
		return "I couldn't put together the morning brief just now."
	}
	return text
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
