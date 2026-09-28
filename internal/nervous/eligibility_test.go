package nervous

import (
	"testing"

	"water/internal/nervous/intents"
	"water/internal/nervous/tmpl"
)

func fixtureShared() intents.Shared {
	return intents.Shared{
		SkipWords:     []string{"please", "hey", "um", "uh", "just"},
		EscalateWords: []string{"should", "why", "prioritize", "which is better", "draft", "never", "don't"},
		ClauseJoiners: []string{"and then", "and also"},
	}
}

func normalizeFor(s string, sh intents.Shared) tmpl.Utterance {
	return tmpl.Normalize(s, wordSet(sh.SkipWords))
}

func TestEligibleEmpty(t *testing.T) {
	sh := fixtureShared()
	u := normalizeFor("please um uh", sh)
	ok, reason := Eligible(u, sh)
	if ok || reason != "empty" {
		t.Fatalf("got ok=%v reason=%q, want ok=false reason=empty", ok, reason)
	}
}

func TestEligibleTooLong(t *testing.T) {
	sh := fixtureShared()
	long := ""
	for i := 0; i < 25; i++ {
		long += "word "
	}
	u := normalizeFor(long, sh)
	ok, reason := Eligible(u, sh)
	if ok || reason != "too_long" {
		t.Fatalf("got ok=%v reason=%q, want ok=false reason=too_long", ok, reason)
	}
}

func TestEligibleEveryEscalateWord(t *testing.T) {
	sh := fixtureShared()
	cases := []string{
		"should i go", "why is this happening", "help me prioritize this",
		"which is better for me", "draft a reply", "never mind that", "don't do it",
	}
	for _, c := range cases {
		u := normalizeFor(c, sh)
		ok, reason := Eligible(u, sh)
		if ok {
			t.Errorf("utterance %q: got eligible=true, want escalate", c)
			continue
		}
		if len(reason) < len("escalate_word:") || reason[:len("escalate_word:")] != "escalate_word:" {
			t.Errorf("utterance %q: got reason=%q, want escalate_word:*", c, reason)
		}
	}
}

func TestEligibleMultiClauseJoiner(t *testing.T) {
	sh := fixtureShared()
	u := normalizeFor("what's on my calendar and then email alex", sh)
	ok, reason := Eligible(u, sh)
	if ok || reason != "multi_clause" {
		t.Fatalf("got ok=%v reason=%q, want ok=false reason=multi_clause", ok, reason)
	}
}

func TestEligibleMultiClauseSentenceMarks(t *testing.T) {
	sh := fixtureShared()
	u := normalizeFor("what's on my calendar today? are you free later?", sh)
	ok, reason := Eligible(u, sh)
	if ok || reason != "multi_clause" {
		t.Fatalf("got ok=%v reason=%q, want ok=false reason=multi_clause", ok, reason)
	}
}

func TestEligibleSingleSentenceMarkWithSecondClauseNotEnough(t *testing.T) {
	// Only one sentence-final mark: the rule requires two or more, so this
	// must NOT trip multi_clause on the sentence-mark path (it also
	// contains no clause_joiners phrase).
	sh := fixtureShared()
	u := normalizeFor("what's on my calendar today? that's all i need", sh)
	ok, reason := Eligible(u, sh)
	if !ok || reason != "" {
		t.Fatalf("a single sentence-final mark must not trip multi_clause: got ok=%v reason=%q", ok, reason)
	}
}

func TestEligibleOK(t *testing.T) {
	sh := fixtureShared()
	u := normalizeFor("what's on my calendar today", sh)
	ok, reason := Eligible(u, sh)
	if !ok || reason != "" {
		t.Fatalf("got ok=%v reason=%q, want ok=true reason=\"\"", ok, reason)
	}
}

func TestEligibleSingleSentenceMarkOK(t *testing.T) {
	sh := fixtureShared()
	u := normalizeFor("what's on my calendar today?", sh)
	ok, reason := Eligible(u, sh)
	if !ok || reason != "" {
		t.Fatalf("a single trailing '?' must not trip multi_clause: got ok=%v reason=%q", ok, reason)
	}
}
