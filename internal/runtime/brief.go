package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"water/internal/backend"
	"water/internal/decisions"
	"water/internal/store"
	"water/internal/twins"
)

// briefSystemPrompt is the one deliberate exception's whole job: the model
// only phrases the signal block computed below, it never decides what
// matters or adds anything to it.
const briefSystemPrompt = "You are writing the CEO's morning brief from a fixed block of signals " +
	"computed by other code, given below as the prompt. Write 3-5 short, plain " +
	"sentences: lead with anything that needs attention today, then the shape " +
	"of the day. Never invent a name, number, subject or event that is not " +
	"literally present in the signal block; if a section is empty, say so " +
	"briefly rather than guessing or filling in something plausible."

// bulkSenderMarkers flags a From address as automated/bulk mail, which the
// "needs attention" heuristic below always excludes regardless of wording.
var bulkSenderMarkers = []string{"no-reply", "noreply", "do-not-reply", "donotreply", "notifications@", "notification@", "mailer-daemon", "newsletter@"}

// attentionMarkers are plain-language signs a message wants a human decision
// or reply soon (a question, a deadline, urgency), not that it was merely
// received.
var attentionMarkers = []string{"?", "asap", "urgent", "deadline", "by eod", "by end of day", "due ", "please respond", "need your", "can you", "could you", "waiting on you"}

// needsAttention is a simple, explainable, non-model heuristic: not from a
// bulk/no-reply-looking sender, and its subject or body reads like it wants a
// reply (a question mark or one of a small set of urgency/deadline phrases).
// It is deliberately conservative and over-simple — a real classifier is not
// worth it for a brief whose only job is to point at a handful of messages,
// not to be exhaustive.
func needsAttention(m store.Message) bool {
	from := strings.ToLower(m.From)
	for _, marker := range bulkSenderMarkers {
		if strings.Contains(from, marker) {
			return false
		}
	}
	text := strings.ToLower(m.Subject + " " + m.Body)
	for _, marker := range attentionMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// briefSignals is everything the model is allowed to talk about, computed
// deterministically in code (StateSummary's pattern, extended with mail).
type briefSignals struct {
	Day              string
	Events           []store.Event
	NewMessages      int
	DistinctSenders  int
	NeedsAttention   []store.Message
	PendingApprovals int
	// OpenCards is absent (nil) when Env.Decisions is unset or returns none:
	// the rendered signal block then simply omits the section, rather than
	// forcing an empty one into the prompt.
	OpenCards []*decisions.Card
	// CardsUnavailable is set when the decision source failed: open cards
	// are an optional signal, so the brief says so instead of failing.
	CardsUnavailable bool
}

// computeBriefSignals reads today's events, mail since yesterday, and the
// pending-approval count, and reports whether any of it is External — the
// same taint rule StateSummary uses. It is cheap (local store reads only, no
// model call, no gate fetch) and safe to call on every request, cache hit or
// not. The open-cards signal is deliberately not here: it runs the decision
// trigger (classification, gate fetches and a phrasing model call per card),
// so doComputeBrief adds it once per day's compute via openCardSignals, and
// the taint it contributes is recorded with the cached brief.
func computeBriefSignals(ctx context.Context, env Env) (briefSignals, bool, error) {
	var sig briefSignals
	var tainted bool
	now := env.now()
	start := startOfDay(now)
	sig.Day = start.Format("2006-01-02")

	events, err := store.EventsInRange(ctx, env.Store, start, start.Add(24*time.Hour))
	if err != nil {
		return sig, false, fmt.Errorf("brief: events: %w", err)
	}
	sig.Events = events
	for _, e := range events {
		tainted = tainted || e.External
	}

	since := start.Add(-24 * time.Hour)
	msgs, err := store.List[store.Message, *store.Message](ctx, env.Store, store.Query{Since: since})
	if err != nil {
		return sig, false, fmt.Errorf("brief: messages: %w", err)
	}
	sig.NewMessages = len(msgs)
	senders := map[string]bool{}
	for _, m := range msgs {
		senders[m.From] = true
		tainted = tainted || m.External
		if needsAttention(m) {
			sig.NeedsAttention = append(sig.NeedsAttention, m)
		}
	}
	sig.DistinctSenders = len(senders)

	if env.Approvals != nil {
		pend, err := env.Approvals.Pending(ctx)
		if err != nil {
			return sig, tainted, fmt.Errorf("brief: approvals: %w", err)
		}
		sig.PendingApprovals = len(pend)
	}
	return sig, tainted, nil
}

// openCardSignals runs the decision source (when one is wired) and returns
// its cards ranked, plus whether any card is Untrusted. It is only called
// when a brief is actually computed, never on a cached-brief ask.
func openCardSignals(ctx context.Context, env Env, now time.Time) ([]*decisions.Card, bool, error) {
	if env.Decisions == nil {
		return nil, false, nil
	}
	cards, err := env.Decisions.Run(ctx, now)
	if err != nil {
		return nil, false, fmt.Errorf("brief: decision cards: %w", err)
	}
	ranked := decisions.Rank(cards)
	tainted := false
	for _, c := range ranked {
		tainted = tainted || c.Untrusted
	}
	return ranked, tainted, nil
}

// briefTaintKey names the per-day record of whether a cached brief was
// built from untrusted content. It lives in the store's generic key/value
// cursor table next to the brief it describes.
func briefTaintKey(day string) string { return "brief_taint:" + day }

// cachedBriefTainted reports the recorded taint of day's cached brief. A
// missing or unreadable record (a brief cached before this record existed,
// or a failed write) counts as tainted: the conservative direction.
func cachedBriefTainted(ctx context.Context, env Env, day string) bool {
	v, ok, err := env.Store.GetCursor(ctx, briefTaintKey(day))
	if err != nil || !ok {
		return true
	}
	return v != "0"
}

// renderBriefSignals turns the computed signals into the model's whole
// prompt: a flat, literal block it is instructed to only reword.
func renderBriefSignals(s briefSignals) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Date: %s\n\n", s.Day)

	fmt.Fprintf(&b, "Today's events (%d):\n", len(s.Events))
	if len(s.Events) == 0 {
		b.WriteString("- none\n")
	}
	for _, e := range s.Events {
		fmt.Fprintf(&b, "- %s %s\n", e.StartAt.Local().Format("15:04"), e.Title)
	}

	fmt.Fprintf(&b, "\nNew messages since yesterday: %d (%d distinct sender(s))\n", s.NewMessages, s.DistinctSenders)
	if len(s.NeedsAttention) == 0 {
		b.WriteString("None flagged as needing a reply.\n")
	} else {
		b.WriteString("Flagged as possibly needing a reply:\n")
		for _, m := range s.NeedsAttention {
			fmt.Fprintf(&b, "- from %s: %s\n", m.From, m.Subject)
		}
	}

	fmt.Fprintf(&b, "\nPending approvals: %d\n", s.PendingApprovals)

	if s.CardsUnavailable {
		b.WriteString("\nOpen decision cards: unavailable right now.\n")
	}

	if len(s.OpenCards) > 0 {
		fmt.Fprintf(&b, "\nOpen decision cards (%d), most important first:\n", len(s.OpenCards))
		for _, c := range s.OpenCards {
			fmt.Fprintf(&b, "- [%s, severity %d, %s] %s\n", c.TypeID, c.Severity, c.Readiness, c.Lead)
		}
	}
	return b.String()
}

// briefWait is one in-flight (or finished) compute for a given day, so a
// background precompute and an on-demand ask racing each other share one
// backend call instead of each starting their own.
type briefWait struct {
	done chan struct{}
	text string
	err  error
}

var (
	briefGuardMu  sync.Mutex
	briefInFlight = map[string]*briefWait{}
)

// ComputeAndCacheBrief returns today's morning brief, computing and caching
// it if nothing is cached yet. Concurrent callers for the same local day
// share one computation (and one backend call): the first caller in computes
// and caches; every other caller for that same day blocks on the same result
// instead of starting its own. This is the sync loop's background precompute
// and FastPath's on-demand path calling into exactly the same place.
func ComputeAndCacheBrief(ctx context.Context, env Env) (string, error) {
	if env.Store == nil {
		return "", errors.New("runtime: brief needs a store")
	}
	day := startOfDay(env.now()).Format("2006-01-02")
	if text, ok, err := env.Store.GetBrief(ctx, day); err != nil {
		return "", fmt.Errorf("brief: reading cache: %w", err)
	} else if ok {
		return text, nil
	}

	briefGuardMu.Lock()
	w, inFlight := briefInFlight[day]
	if !inFlight {
		w = &briefWait{done: make(chan struct{})}
		briefInFlight[day] = w
	}
	briefGuardMu.Unlock()

	if inFlight {
		select {
		case <-w.done:
			return w.text, w.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	w.text, w.err = doComputeBrief(ctx, env, day)

	briefGuardMu.Lock()
	delete(briefInFlight, day)
	briefGuardMu.Unlock()
	close(w.done)

	return w.text, w.err
}

// doComputeBrief is the actual, uncached computation: signals (including
// the open-cards signal, run once here), one model call on the non-warm
// backend path, then a cache write together with the brief's taint. Only
// ComputeAndCacheBrief's compute-once guard calls this.
func doComputeBrief(ctx context.Context, env Env, day string) (string, error) {
	if env.Manifest == nil {
		return "", errors.New("runtime: brief needs a manifest")
	}
	sig, tainted, err := computeBriefSignals(ctx, env)
	if err != nil {
		return "", err
	}
	cards, cardsTainted, cerr := openCardSignals(ctx, env, env.now())
	if cerr != nil {
		// Optional signal: the brief still goes out, and says the cards
		// could not be read rather than pretending there are none.
		sig.CardsUnavailable = true
	} else {
		sig.OpenCards = cards
		tainted = tainted || cardsTainted
	}
	req := backend.Request{
		System:  briefSystemPrompt,
		Prompt:  renderBriefSignals(sig),
		Role:    "ceo",
		Model:   env.Manifest.ModelFor(twins.TierFast),
		Timeout: env.timeout(),
	}
	// Never the warm session: the brief's system prompt and (absent) tools
	// differ from the chat's, so running it there would kill the chat's
	// process and its conversation, and the next chat turn would kill the
	// brief's in turn.
	resp, err := streamCold(ctx, env, req, func(string) {})
	if err != nil {
		return "", fmt.Errorf("brief: %w", err)
	}
	text := strings.TrimSpace(resp.Text)
	// The taint record goes first, so a cached brief never exists without
	// one (a failed write leaves it missing, which reads as tainted).
	taintVal := "0"
	if tainted {
		taintVal = "1"
	}
	_ = env.Store.SetCursor(ctx, briefTaintKey(day), taintVal)
	if err := env.Store.SetBrief(ctx, day, text); err != nil {
		return "", fmt.Errorf("brief: caching: %w", err)
	}
	return text, nil
}
