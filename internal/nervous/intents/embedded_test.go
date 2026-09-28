package intents_test

import (
	"net/mail"
	"regexp"
	"strings"
	"testing"
	"time"

	"water"
	"water/internal/nervous/eval"
	"water/internal/nervous/intents"
	"water/internal/nervous/propose"
	"water/internal/nervous/reflex"
	"water/internal/nervous/slots"
	"water/internal/nervous/tmpl"
	"water/internal/twins"
)

// ---- a minimal, test-only matcher over an *intents.Registry, used only to
// verify the CEO intent files' own tests: entries and the legacy
// fast-path regression set. It deliberately does NOT implement
// specificity tie-breaking, the pre-tier eligibility gate (escalate_words,
// multi_clause, too_long), or a circuit breaker — that is the real Tier 0
// adapter's job (task R-11). This is just enough to prove the DATA in
// twins/ceo/intents/*.yaml behaves as intended. This file is an external
// (_test) package, not internal to package intents, specifically so it can
// import internal/nervous/reflex (which itself imports intents) without an
// import cycle.

func toSet(words []string) map[string]bool {
	m := make(map[string]bool, len(words))
	for _, w := range words {
		m[w] = true
	}
	return m
}

func fixtureEntities(t testing.TB) slots.Entities {
	t.Helper()
	fx := eval.DefaultFixture()
	people := make([]slots.Person, 0, len(fx.Senders))
	for _, s := range fx.Senders {
		addr, err := mail.ParseAddress(s)
		if err != nil {
			t.Fatalf("fixture sender %q: %v", s, err)
		}
		people = append(people, slots.Person{Name: addr.Name, Email: addr.Address})
	}
	return slots.Entities{People: people}
}

// matchOneTemplate tries it's own templates only, resolving every declared
// slot (captured, or defaulted when the template didn't capture it). The
// first template with a fully-resolving candidate wins.
func matchOneTemplate(reg *intents.Registry, it intents.Intent, u tmpl.Utterance, deny map[string]bool, ents slots.Entities, now time.Time) (map[string]string, bool) {
	for _, tp := range reg.Templates(it.ID) {
		var result map[string]string
		found := false
		tp.MatchAll(u, func(m tmpl.Match) bool {
			lit := make(map[string]bool, len(m.LiteralSet))
			for _, w := range m.LiteralSet {
				lit[w] = true
			}
			for _, tok := range u.Tokens {
				if deny[tok] && !lit[tok] {
					return false
				}
			}
			labels := map[string]string{}
			captured := map[string]bool{}
			for _, c := range m.Captures {
				spec, ok := it.Slots[c.Slot]
				if !ok {
					return false
				}
				v, outcome, _ := slots.Resolve(spec.Type, c, slots.Spec{Min: spec.Min, Max: spec.Max}, now, ents)
				if outcome != slots.Resolved {
					return false
				}
				labels[c.Slot] = v.Label
				captured[c.Slot] = true
			}
			for name, spec := range it.Slots {
				if captured[name] || spec.Default == "" {
					continue
				}
				v, outcome, _ := slots.ResolveString(spec.Type, spec.Default, slots.Spec{Min: spec.Min, Max: spec.Max}, now, ents)
				if outcome == slots.Resolved {
					labels[name] = v.Label
				}
			}
			result = labels
			found = true
			return true
		})
		if found {
			return result, true
		}
	}
	return nil, false
}

// matchAny tries every candidate intent (respecting requires_pending), in
// registry order, and returns the first that matches. It does not resolve
// ambiguity between two matching intents — for this test file's purposes
// (does ANY intent answer, or does NONE), that distinction doesn't matter.
func matchAny(reg *intents.Registry, utterance string, pending int, ents slots.Entities, now time.Time) (string, map[string]string, bool) {
	shared := reg.Shared()
	u := tmpl.Normalize(utterance, toSet(shared.SkipWords))
	deny := toSet(shared.DenyWords)
	for _, it := range reg.Candidates() {
		if it.RequiresPending && pending == 0 {
			continue
		}
		if labels, ok := matchOneTemplate(reg, it, u, deny, ents, now); ok {
			return it.ID, labels, true
		}
	}
	return "", nil, false
}

func slotsMatchLenient(got, want map[string]string) bool {
	for k, v := range want {
		gv, ok := got[k]
		if !ok {
			continue
		}
		if strings.TrimSpace(strings.ToLower(gv)) != strings.TrimSpace(strings.ToLower(v)) {
			return false
		}
	}
	return true
}

func loadEmbeddedCEO(t testing.TB) (*intents.Registry, *twins.Manifest) {
	t.Helper()
	m, err := twins.Load(water.TwinsFS(), "ceo")
	if err != nil {
		t.Fatalf("twins.Load(ceo): %v", err)
	}
	reg, err := intents.LoadRegistry(water.TwinsFS(), m, intents.Functions{Read: reflex.Specs(), Write: propose.Specs()}, intents.LoadOptions{})
	if err != nil {
		t.Fatalf("LoadRegistry(ceo): %v", err)
	}
	return reg, m
}

var fixtureNow = time.Date(2026, 9, 24, 9, 0, 0, 0, time.FixedZone("PT", -7*3600)) // Thursday

func TestEmbeddedCEOIntentsLoad(t *testing.T) {
	reg, _ := loadEmbeddedCEO(t)
	want := []string{
		"schedule.on_date", "schedule.next_event", "schedule.free_time",
		"mail.latest", "mail.unread_count", "mail.latest_from",
		"brief.today", "approvals.list", "approvals.respond",
		"control.stop", "status.overview", "help.intents",
		// The four write intents (R-20): all are active in the real ceo
		// manifest, which grants gcal.create_event/move_event and
		// gmail.send_message at level A and gmail.draft_message at level D
		// (docs/slice-c-planning.md's write-function increment, landed by
		// the concurrent session; see docs/slices/R.md Risk item 24).
		"calendar.create_event", "calendar.move_event",
		"mail.draft_reply", "mail.send_reply",
	}
	got := map[string]bool{}
	for _, it := range reg.Candidates() {
		got[it.ID] = true
		if !it.Active {
			t.Errorf("%s: expected Active, got inactive (%s)", it.ID, it.InactiveReason)
		}
	}
	for _, id := range want {
		if !got[id] {
			t.Errorf("expected intent %q in the embedded ceo registry, not found", id)
		}
	}
	if len(got) != len(want) {
		t.Errorf("Candidates() has %d intents, want exactly %d (%v)", len(got), len(want), got)
	}
}

func TestIntentFileTests(t *testing.T) {
	reg, _ := loadEmbeddedCEO(t)
	ents := fixtureEntities(t)

	for _, it := range reg.Candidates() {
		it := it
		for i, tc := range it.Tests {
			t.Run(it.ID+"/"+tc.Utterance, func(t *testing.T) {
				gotID, gotSlots, ok := matchAny(reg, tc.Utterance, tc.Pending, ents, fixtureNow)
				if tc.Intent == "none" {
					if ok {
						t.Fatalf("test[%d]: %q matched %s, want no match", i, tc.Utterance, gotID)
					}
					return
				}
				if !ok {
					t.Fatalf("test[%d]: %q did not match anything, want %s", i, tc.Utterance, tc.Intent)
				}
				if gotID != tc.Intent {
					t.Fatalf("test[%d]: %q matched %s, want %s", i, tc.Utterance, gotID, tc.Intent)
				}
				if !slotsMatchLenient(gotSlots, tc.Slots) {
					t.Fatalf("test[%d]: %q slots = %v, want (leniently) %v", i, tc.Utterance, gotSlots, tc.Slots)
				}
			})
		}
	}
}

func TestLegacyFastPathCases(t *testing.T) {
	reg, _ := loadEmbeddedCEO(t)
	ents := fixtureEntities(t)

	positives := map[string]string{
		"what's on my calendar today":    "schedule.on_date",
		"what's on my calendar tomorrow": "schedule.on_date",
		"do I have any meetings today":   "schedule.on_date",
		"any pending approvals":          "approvals.list",
		"what needs my approval":         "approvals.list",
		"is my morning brief ready":      "brief.today",
	}
	for u, want := range positives {
		gotID, _, ok := matchAny(reg, u, 0, ents, fixtureNow)
		if !ok || gotID != want {
			t.Errorf("legacy positive %q: got (%s, %v), want %s", u, gotID, ok, want)
		}
	}

	nearMisses := []string{
		"help me schedule a meeting with the calendar team",
		"please schedule a meeting with bob tomorrow",
		"can you approve this for me",
		"tell me about the weather",
		"write a brief history of the company",
	}
	for _, u := range nearMisses {
		if gotID, _, ok := matchAny(reg, u, 0, ents, fixtureNow); ok {
			t.Errorf("legacy near-miss %q: matched %s, want no match", u, gotID)
		}
	}
}

// TestEvalNoLeak proves the held-out eval set (internal/nervous/eval) was
// not, even accidentally, copied into the intent files' own examples: or
// tests: fields (which would make it not actually held out — see
// docs/slices/R.md Risks item 19).
func TestEvalNoLeak(t *testing.T) {
	reg, _ := loadEmbeddedCEO(t)
	shared := reg.Shared()
	skip := toSet(shared.SkipWords)

	fromIntentFiles := map[string]bool{}
	for _, it := range reg.Intents() {
		for _, ex := range it.Examples {
			u := tmpl.Normalize(ex, skip)
			fromIntentFiles[strings.Join(u.Tokens, " ")] = true
		}
		for _, tc := range it.Tests {
			u := tmpl.Normalize(tc.Utterance, skip)
			fromIntentFiles[strings.Join(u.Tokens, " ")] = true
		}
	}

	cases, err := eval.LoadCEOEval()
	if err != nil {
		t.Fatalf("eval.LoadCEOEval: %v", err)
	}
	leaked := 0
	for _, c := range cases {
		u := tmpl.Normalize(c.Utterance, skip)
		key := strings.Join(u.Tokens, " ")
		if fromIntentFiles[key] {
			t.Errorf("eval case %q is verbatim (after normalization) in an intent file's examples/tests", c.Utterance)
			leaked++
		}
	}
	if leaked > 0 {
		t.Fatalf("%d eval case(s) leaked into twins/ceo/intents/*.yaml", leaked)
	}
}

var slotOrRuleRe = regexp.MustCompile(`\{[^}]*\}|<[^>]*>`)
var grammarPunctRe = regexp.MustCompile(`[()\[\]|]`)

// roughLiteralWords is a from-scratch reimplementation (not a call into)
// intents' own unexported literalWords, so this test doesn't just prove
// that helper agrees with itself. Same idea: strip {slot}/<rule> spans and
// grouping punctuation, leaving only a template's literal wording.
func roughLiteralWords(src string) string {
	s := slotOrRuleRe.ReplaceAllString(src, " ")
	s = grammarPunctRe.ReplaceAllString(s, " ")
	return s
}

// TestNoEscalateWordInTemplates is a second, independent check that no
// embedded template's literal wording contains an escalate word —
// intents.LoadRegistry's internal checkTemplateVocabulary already enforces
// this at load time (so a violation would have failed loadEmbeddedCEO
// above), but this scans the raw source files directly with an
// independently written literal-word extractor, so a bug in
// checkTemplateVocabulary itself wouldn't silently go uncaught.
func TestNoEscalateWordInTemplates(t *testing.T) {
	reg, _ := loadEmbeddedCEO(t)
	shared := reg.Shared()
	skip := toSet(shared.SkipWords)
	for _, it := range reg.Intents() {
		for _, src := range it.Templates {
			literal := roughLiteralWords(src)
			u := tmpl.Normalize(literal, skip)
			joined := " " + strings.Join(u.Tokens, " ") + " "
			for _, w := range shared.EscalateWords {
				pw := tmpl.Normalize(w, nil)
				phrase := " " + strings.Join(pw.Tokens, " ") + " "
				if strings.TrimSpace(phrase) != "" && strings.Contains(joined, phrase) {
					t.Errorf("%s: template %q's literal wording contains escalate word/phrase %q", it.ID, src, w)
				}
			}
		}
	}
}

func TestDemoTwinLoads(t *testing.T) {
	m, err := twins.Load(water.TwinsFS(), "ceo-demo")
	if err != nil {
		t.Fatalf("twins.Load(ceo-demo): %v", err)
	}
	reg, err := intents.LoadRegistry(water.TwinsFS(), m, intents.Functions{Read: reflex.Specs()}, intents.LoadOptions{})
	if err != nil {
		t.Fatalf("LoadRegistry(ceo-demo): %v", err)
	}
	if len(reg.Candidates()) != 0 {
		t.Fatalf("ceo-demo has no twins/ceo-demo/intents dir; want an empty registry, got %d candidates", len(reg.Candidates()))
	}
}
