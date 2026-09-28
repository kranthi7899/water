package approvals

import "testing"

// Slice W, D5a: natural phrasings approve only when exactly one envelope is
// pending. Every one of these is Ambiguous under Match today (the incident's
// "I approve the message" included), which on the voice path is applied as a
// denial — so each would have rejected the send it meant to approve.
func TestMatchPendingNaturalPhrasingsWithOnePending(t *testing.T) {
	yes := []string{
		"I approve the message",
		"I approve the message.",
		"i approve",
		"I approve it",
		"approve the message",
		"approve the email",
		"yes send it",
		"Yes, send it.",
		"send it",
		"go ahead",
		"Go ahead and send it",
		"do it",
		"just do it",
		"yes go ahead",
		"confirm send",
		"confirm send it",
		"yeah we can send it",
		"yes that's fine",
	}
	for _, r := range yes {
		if got := MatchPending(r, 1); got != Yes {
			t.Errorf("MatchPending(%q, 1) = %v, want Yes", r, got)
		}
	}
}

func TestMatchPendingIsMatchUnlessExactlyOnePending(t *testing.T) {
	replies := []string{
		"I approve the message", "go ahead", "do it", "send it", "yes send it",
		"yes", "no", "approve it", "nope", "maybe", "", "correct that",
	}
	for _, pending := range []int{0, 2, 3} {
		for _, r := range replies {
			if got, want := MatchPending(r, pending), Match(r); got != want {
				t.Errorf("MatchPending(%q, %d) = %v, want Match's %v", r, pending, got, want)
			}
		}
	}
	// And the widened phrasings specifically are NOT yes without exactly one pending.
	for _, r := range []string{"I approve the message", "go ahead", "do it", "send it"} {
		if MatchPending(r, 0) == Yes || MatchPending(r, 2) == Yes {
			t.Errorf("%q must not be Yes with 0 or 2 pending", r)
		}
	}
}

func TestMatchPendingNegationWins(t *testing.T) {
	cases := map[string]Answer{
		"don't send it":          No,
		"do not send it":         No,
		"don't do it":            No,
		"don't go ahead":         No,
		"no":                     No,
		"no don't":               No,
		"go ahead, no wait":      Ambiguous,
		"yes no":                 Ambiguous,
		"send it, actually stop": Ambiguous,
		"that's not fine":        Ambiguous,
	}
	for r, want := range cases {
		if got := MatchPending(r, 1); got != want {
			t.Errorf("MatchPending(%q, 1) = %v, want %v", r, got, want)
		}
	}
}

func TestMatchPendingUnknownWordsStayAmbiguous(t *testing.T) {
	for _, r := range []string{
		"correct that",
		"please correct it",
		"send it to bob",
		"yes send it to bob",
		"yes but change the subject",
		"go ahead later",
		"maybe",
		"",
	} {
		if got := MatchPending(r, 1); got == Yes {
			t.Errorf("MatchPending(%q, 1) = Yes, want not Yes", r)
		}
	}
}
