package nervous

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	water "water"
	"water/internal/backend"
	"water/internal/nervous/intents"
	"water/internal/nervous/render"
	"water/internal/runtime"
)

// Slice W, D3: the handoff is never a spoken sentence by default. A voice
// turn escalated to the main model emits a silent ack (so the globe keeps
// its THINKING pulse) and the first sentence event is the model's own text.
// router.voice_filler_ms (Config.VoiceFiller) optionally speaks ONE short
// filler, only if nothing has arrived by then.

// stepBackend is a streaming test backend the test drives one delta at a
// time, so the fake clock can be advanced while a main turn is genuinely
// still in flight (backend.Fake emits its whole reply at once).
type stepBackend struct {
	started chan struct{}
	steps   chan string   // each value is emitted as one delta; close to finish
	stepped chan struct{} // one send per delta, after onDelta returned
	once    sync.Once
}

func newStepBackend() *stepBackend {
	return &stepBackend{started: make(chan struct{}), steps: make(chan string), stepped: make(chan struct{})}
}

func (b *stepBackend) Name() string { return "step" }
func (b *stepBackend) Available(context.Context) backend.Availability {
	return backend.Availability{Installed: true, Authed: true}
}
func (b *stepBackend) Run(ctx context.Context, req backend.Request) (backend.Response, error) {
	return b.RunStream(ctx, req, nil)
}
func (b *stepBackend) RunStream(ctx context.Context, req backend.Request, onDelta func(string)) (backend.Response, error) {
	b.once.Do(func() { close(b.started) })
	var sb strings.Builder
	for s := range b.steps {
		sb.WriteString(s)
		if onDelta != nil {
			onDelta(s)
		}
		b.stepped <- struct{}{}
	}
	return backend.Response{Text: sb.String(), Backend: "step"}, nil
}

// delta sends one delta and waits until it has been delivered.
func (b *stepBackend) delta(t *testing.T, s string) {
	t.Helper()
	select {
	case b.steps <- s:
	case <-time.After(2 * time.Second):
		t.Fatal("backend never consumed the step")
	}
	<-b.stepped
}

type fillerRun struct {
	n      *Nervous
	env    runtime.Env
	clock  *fakeClock
	b      *stepBackend
	mu     sync.Mutex
	events []runtime.Event
	done   chan struct{}
}

// startFillerTurn starts a main-path turn ("should" forces escalation) on ch
// with the given filler delay and returns once the backend is streaming.
func startFillerTurn(t *testing.T, ch runtime.Channel, filler time.Duration, style *render.Style) *fillerRun {
	t.Helper()
	reg := tier0FixtureRegistry(t, map[string]string{"schedule_on_date": tier0ScheduleYAML})
	env, ctx, _ := nervousTestEnv(t)
	b := newStepBackend()
	env.Backend = b
	clock := newFakeClock(tier0FixedNow)
	if style == nil {
		style = render.DefaultStyle()
	}
	n, err := New(Config{
		Registry:     func() *intents.Registry { return reg },
		Style:        style,
		Tier0Enabled: true,
		MainEnabled:  true,
		Clock:        clock,
		AckAfter:     DefaultAckAfter,
		VoiceFiller:  filler,
		Store:        env.Store,
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &fillerRun{n: n, env: env, clock: clock, b: b, done: make(chan struct{})}
	go func() {
		defer close(r.done)
		n.Handle(ctx, env, Turn{Channel: ch, Text: "what should i say about the board", TaskID: "filler-" + string(ch)}, collect(&r.events, &r.mu))
	}()
	select {
	case <-b.started:
	case <-time.After(2 * time.Second):
		t.Fatal("main path never reached the backend")
	}
	return r
}

func (r *fillerRun) finish(t *testing.T) []runtime.Event {
	t.Helper()
	close(r.b.steps)
	select {
	case <-r.done:
	case <-time.After(2 * time.Second):
		t.Fatal("turn never finished")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]runtime.Event(nil), r.events...)
}

func (r *fillerRun) snapshot() []runtime.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]runtime.Event(nil), r.events...)
}

func sentences(events []runtime.Event) []string {
	var out []string
	for _, e := range events {
		if e.Kind == runtime.EventSentence {
			out = append(out, e.Text)
		}
	}
	return out
}

// TestVoiceMainTurnSpeaksNoFiller: with the default config (no filler), a
// voice turn escalated to main emits no sentence before the model's first
// delta, and the handoff is a silent ack after the router's own ack. Before
// Slice W this failed: emitHandoff spoke style.voice.handoff[0] ("One
// moment.") as a sentence at ~5ms on every escalated voice turn.
func TestVoiceMainTurnSpeaksNoFiller(t *testing.T) {
	r := startFillerTurn(t, runtime.ChannelVoice, 0, nil)
	// Nothing from the model yet, and a long wait: still nothing spoken.
	r.clock.Advance(10 * time.Second)
	before := r.snapshot()
	if s := sentences(before); len(s) != 0 {
		t.Fatalf("spoke %q before the model produced anything; the handoff must be silent", s)
	}
	acks := 0
	for _, e := range before {
		if e.Kind != runtime.EventAck {
			t.Fatalf("event %q before main output, want only acks: %+v", e.Kind, before)
		}
		acks++
	}
	if acks != 2 {
		t.Fatalf("acks before main output = %d, want 2 (router ack + silent handoff ack): %+v", acks, before)
	}

	r.b.delta(t, "Board sync is on Tuesday.")
	events := r.finish(t)
	got := sentences(events)
	if len(got) == 0 || got[0] != "Board sync is on Tuesday." {
		t.Fatalf("first spoken sentence = %q, want the model's own text", got)
	}
	row := lastRoute(t, r.env.Store, tier0FixedNow.Add(-time.Hour))
	if row.AckMS == nil {
		t.Fatal("route_log ack_ms is null; the silent handoff must still be recorded")
	}
}

// TestVoiceHandoffNeverSpokenWithEmbeddedStyle pins the same rule against
// the real embedded twins/ceo/style.yaml (whose handoff list used to open
// with "One moment."), not just render.DefaultStyle.
func TestVoiceHandoffNeverSpokenWithEmbeddedStyle(t *testing.T) {
	style, err := render.LoadStyle(water.TwinsFS(), "ceo")
	if err != nil {
		t.Fatalf("load ceo style: %v", err)
	}
	for _, h := range style.Voice().Handoff {
		if strings.EqualFold(strings.TrimSpace(h), "One moment.") {
			t.Fatalf("ceo style.yaml still carries the reflexive %q handoff", h)
		}
	}
	r := startFillerTurn(t, runtime.ChannelVoice, 0, style)
	r.clock.Advance(5 * time.Second)
	if s := sentences(r.snapshot()); len(s) != 0 {
		t.Fatalf("spoke %q with the embedded ceo style; the handoff must be silent", s)
	}
	r.finish(t)
}

// TestVoiceFillerKnob covers router.voice_filler_ms (Config.VoiceFiller).
func TestVoiceFillerKnob(t *testing.T) {
	phrase := render.DefaultStyle().Voice().Handoff[0]

	t.Run("fires once when nothing arrived", func(t *testing.T) {
		r := startFillerTurn(t, runtime.ChannelVoice, 1500*time.Millisecond, nil)
		r.clock.Advance(1499 * time.Millisecond)
		if s := sentences(r.snapshot()); len(s) != 0 {
			t.Fatalf("filler spoke early: %q", s)
		}
		r.clock.Advance(time.Millisecond)
		r.clock.Advance(5 * time.Second)
		if s := sentences(r.snapshot()); len(s) != 1 || s[0] != phrase {
			t.Fatalf("sentences after the filler delay = %q, want exactly [%q]", s, phrase)
		}
		r.clock.Advance(500 * time.Millisecond)
		r.b.delta(t, "Here it is.")
		events := r.finish(t)
		s := sentences(events)
		if len(s) != 2 || s[0] != phrase || s[1] != "Here it is." {
			t.Fatalf("sentences = %q, want [filler, model text]", s)
		}
		// first_sentence_ms ignores the filler: it measures the model's
		// own first delta (at 7000ms), not the filler (at 1500ms).
		row := lastRoute(t, r.env.Store, tier0FixedNow.Add(-time.Hour))
		if row.FirstSentenceMS == nil || *row.FirstSentenceMS != 7000 {
			var got any = nil
			if row.FirstSentenceMS != nil {
				got = *row.FirstSentenceMS
			}
			t.Fatalf("first_sentence_ms = %v, want 7000 (the model's text, not the filler)", got)
		}
	})

	t.Run("suppressed by an earlier delta", func(t *testing.T) {
		r := startFillerTurn(t, runtime.ChannelVoice, 1500*time.Millisecond, nil)
		r.clock.Advance(1000 * time.Millisecond)
		r.b.delta(t, "Working on ")
		r.clock.Advance(5 * time.Second)
		r.b.delta(t, "it now.")
		events := r.finish(t)
		for _, s := range sentences(events) {
			if s == phrase {
				t.Fatalf("filler spoke after the model already produced output: %q", sentences(events))
			}
		}
	})

	t.Run("zero means off", func(t *testing.T) {
		r := startFillerTurn(t, runtime.ChannelVoice, 0, nil)
		r.clock.Advance(30 * time.Second)
		if s := sentences(r.snapshot()); len(s) != 0 {
			t.Fatalf("filler spoke with voice_filler_ms=0: %q", s)
		}
		r.finish(t)
	})

	for _, ch := range []runtime.Channel{runtime.ChannelTextBar, runtime.ChannelCLI} {
		t.Run("never on "+string(ch), func(t *testing.T) {
			r := startFillerTurn(t, ch, 1500*time.Millisecond, nil)
			r.clock.Advance(10 * time.Second)
			for _, e := range r.snapshot() {
				if e.Kind == runtime.EventSentence || e.Kind == runtime.EventDelta {
					t.Fatalf("filler reached %s: %+v", ch, e)
				}
			}
			r.finish(t)
		})
	}

	t.Run("never after the turn ended", func(t *testing.T) {
		r := startFillerTurn(t, runtime.ChannelVoice, 1500*time.Millisecond, nil)
		events := r.finish(t) // model returned nothing at all, before the delay
		r.clock.Advance(10 * time.Second)
		after := r.snapshot()
		if len(after) != len(events) {
			t.Fatalf("events emitted after the turn finished: %+v", after[len(events):])
		}
	})
}
