package nervous

import (
	"io/fs"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"water"
	"water/internal/approvals"
)

// TestVoiceApprovalTierTable covers every rule of Design §13's table, first
// match wins, including the specific edge cases R-21 calls out explicitly.
func TestVoiceApprovalTierTable(t *testing.T) {
	internal := []string{"acme.com"}

	cases := []struct {
		name       string
		env        approvals.Envelope
		domains    []string
		wantTier   VoiceTier
		wantReason string
	}{
		{
			name: "rule1 P2 origin taps regardless of risk",
			env: approvals.Envelope{
				Origin: "p2", Risk: "low", Action: "gcal.create_event",
				Payload: map[string]any{"attendees": []string{"a@acme.com"}},
			},
			domains: internal, wantTier: TapRequired, wantReason: "auto-mode origin",
		},
		{
			name: "rule2 high risk taps",
			env: approvals.Envelope{
				Origin: "p0", Risk: "high", Action: "gcal.create_event",
				Payload: map[string]any{"attendees": []string{"a@acme.com"}},
			},
			domains: internal, wantTier: TapRequired, wantReason: "declared high risk",
		},
		{
			name: "rule3 empty risk taps (unrated)",
			env: approvals.Envelope{
				Origin: "p0", Risk: "", Action: "gcal.create_event",
				Payload: map[string]any{"attendees": []string{"a@acme.com"}},
			},
			domains: internal, wantTier: TapRequired, wantReason: "unrated",
		},
		{
			name: "rule3 unrecognized risk taps (unrated)",
			env: approvals.Envelope{
				Origin: "p0", Risk: "critical", Action: "gcal.create_event",
				Payload: map[string]any{"attendees": []string{"a@acme.com"}},
			},
			domains: internal, wantTier: TapRequired, wantReason: "unrated",
		},
		{
			name: "gmail.send_message on p0 is never a plain voice yes: two-step confirm (Slice W, D5b)",
			env: approvals.Envelope{
				Origin: "p0", Risk: "low", Action: "gmail.send_message",
				Payload: map[string]any{"to": []string{"a@acme.com"}},
			},
			domains: internal, wantTier: VoiceConfirm, wantReason: "",
		},
		{
			name: "rule4 unknown action taps",
			env: approvals.Envelope{
				Origin: "p0", Risk: "low", Action: "gdrive.share_file",
			},
			domains: internal, wantTier: TapRequired, wantReason: "not voice-eligible",
		},
		{
			name: "rule5 external attendee taps even for gcal.create_event at low risk",
			env: approvals.Envelope{
				Origin: "p0", Risk: "low", Action: "gcal.create_event",
				Payload: map[string]any{"attendees": []string{"a@acme.com", "b@evil.example"}},
			},
			domains: internal, wantTier: TapRequired, wantReason: "external recipient",
		},
		{
			name: "rule5 empty internal domains: every recipient is external",
			env: approvals.Envelope{
				Origin: "p0", Risk: "low", Action: "gcal.create_event",
				Payload: map[string]any{"attendees": []string{"a@acme.com"}},
			},
			domains: nil, wantTier: TapRequired, wantReason: "external recipient",
		},
		{
			name: "rule5 empty internal domains but zero recipients: not external",
			env: approvals.Envelope{
				Origin: "p0", Risk: "low", Action: "gcal.create_event",
			},
			domains: nil, wantTier: VoiceYes, wantReason: "",
		},
		{
			name: "rule5 e.Recipient itself external",
			env: approvals.Envelope{
				Origin: "p0", Risk: "medium", Action: "gcal.move_event", Recipient: "a@evil.example",
			},
			domains: internal, wantTier: TapRequired, wantReason: "external recipient",
		},
		{
			name: "rule6 low risk, voice-eligible, internal-only recipients: VoiceYes",
			env: approvals.Envelope{
				Origin: "p0", Risk: "low", Action: "gcal.create_event",
				Payload: map[string]any{"attendees": []string{"a@acme.com", "b@acme.com"}},
			},
			domains: internal, wantTier: VoiceYes, wantReason: "",
		},
		{
			name: "rule6 medium risk, voice-eligible, internal-only recipients: VoiceYes",
			env: approvals.Envelope{
				Origin: "p0", Risk: "medium", Action: "gmail.draft_message",
				Payload: map[string]any{"to": []any{"a@acme.com"}},
			},
			domains: internal, wantTier: VoiceYes, wantReason: "",
		},
		{
			name: "domain match is case-insensitive",
			env: approvals.Envelope{
				Origin: "p0", Risk: "low", Action: "gcal.create_event",
				Payload: map[string]any{"attendees": []string{"a@ACME.COM"}},
			},
			domains: internal, wantTier: VoiceYes, wantReason: "",
		},
		{
			name: "an address with no domain is treated as external",
			env: approvals.Envelope{
				Origin: "p0", Risk: "low", Action: "gcal.move_event", Recipient: "twin:xyz",
			},
			domains: internal, wantTier: TapRequired, wantReason: "external recipient",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tier, reason := VoiceApprovalTier(c.env, c.domains)
			if tier != c.wantTier {
				t.Errorf("tier = %v, want %v", tier, c.wantTier)
			}
			if c.wantReason != "" && reason != c.wantReason {
				t.Errorf("reason = %q, want %q", reason, c.wantReason)
			}
			if c.wantTier == VoiceYes && reason != "" {
				t.Errorf("VoiceYes should carry no reason, got %q", reason)
			}
		})
	}
}

func TestVoiceApprovalTierRuleOrderFirstMatchWins(t *testing.T) {
	// P2 origin wins even though risk is also high and the action is also
	// ineligible: rule 1 must short-circuit before rules 2-4 are even
	// considered.
	env := approvals.Envelope{Origin: "p2", Risk: "high", Action: "gmail.send_message"}
	tier, reason := VoiceApprovalTier(env, nil)
	if tier != TapRequired || reason != "auto-mode origin" {
		t.Fatalf("got tier=%v reason=%q, want TapRequired/auto-mode origin", tier, reason)
	}
}

// Slice W, D5b: the two-step spoken send tier, for gmail.send_message and
// twinlink.send_message only, P0 only, never with recipient warnings.
func TestVoiceApprovalTierConfirmSend(t *testing.T) {
	mail := map[string]any{"to": []any{"kranthi@gmail.com"}, "subject": "Hi"}
	twin := map[string]any{"to_twin": "acme-ceo", "subject": "Hi", "type": "request"}
	cases := []struct {
		name    string
		env     approvals.Envelope
		want    VoiceTier
		wantWhy string
	}{
		{"gmail p0 clean", approvals.Envelope{Action: "gmail.send_message", Origin: "p0", Risk: "high", Payload: mail}, VoiceConfirm, ""},
		{"twinlink p0 clean", approvals.Envelope{Action: "twinlink.send_message", Origin: "p0", Risk: "high", Payload: twin}, VoiceConfirm, ""},
		{"gmail p2", approvals.Envelope{Action: "gmail.send_message", Origin: "p2", Risk: "high", Payload: mail}, TapRequired, "auto-mode origin"},
		{"twinlink p2", approvals.Envelope{Action: "twinlink.send_message", Origin: "p2", Risk: "high", Payload: twin}, TapRequired, "auto-mode origin"},
		{"gmail p0 with warnings", approvals.Envelope{Action: "gmail.send_message", Origin: "p0", Risk: "high", Payload: mail, Warnings: []string{"therightgmail.com has no mail server"}}, TapRequired, "recipient warnings"},
		{"twinlink p0 with warnings", approvals.Envelope{Action: "twinlink.send_message", Origin: "p0", Risk: "high", Payload: twin, Warnings: []string{"x"}}, TapRequired, "recipient warnings"},
		{"gmail p0 no recipient", approvals.Envelope{Action: "gmail.send_message", Origin: "p0", Risk: "high", Payload: map[string]any{"subject": "Hi"}}, TapRequired, "no recipient"},
		{"gmail other origin", approvals.Envelope{Action: "gmail.send_message", Origin: "p1", Risk: "high", Payload: mail}, TapRequired, "declared high risk"},
		{"gmail empty origin", approvals.Envelope{Action: "gmail.send_message", Risk: "high", Payload: mail}, TapRequired, "declared high risk"},
		// Everything else is unchanged: money, public posts and other
		// high-risk or unlisted actions stay tap-only.
		{"money-shaped", approvals.Envelope{Action: "stripe.create_payment", Origin: "p0", Risk: "high", Payload: map[string]any{"amount": 100}}, TapRequired, "declared high risk"},
		{"public post", approvals.Envelope{Action: "linkedin.post", Origin: "p0", Risk: "medium", Payload: map[string]any{"text": "hi"}}, TapRequired, "not voice-eligible"},
		{"gmail draft to external", approvals.Envelope{Action: "gmail.draft_message", Origin: "p0", Risk: "low", Payload: mail}, TapRequired, "external recipient"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, why := VoiceApprovalTier(c.env, []string{"acme.com"})
			if got != c.want || why != c.wantWhy {
				t.Fatalf("tier=%v why=%q, want %v %q", got, why, c.want, c.wantWhy)
			}
		})
	}
}

// Finding 4's deny trap: on the voice path anything that isn't Yes is
// applied as a denial, so every affirmative template of the real
// approvals.respond intent must read as Yes under MatchPending with one
// envelope pending, and every negative one must not.
func TestApprovalsRespondTemplatesAgreeWithMatchPending(t *testing.T) {
	raw, err := fs.ReadFile(water.TwinsFS(), "twins/ceo/intents/approvals_respond.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Templates []string `yaml:"templates"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	negativeMarkers := []string{"no", "nope", "deny", "denied", "reject", "skip"}
	isNegative := func(tpl string) bool {
		for _, w := range strings.Fields(tpl) {
			for _, m := range negativeMarkers {
				if w == m {
					return true
				}
			}
		}
		return false
	}
	if len(doc.Templates) < 20 {
		t.Fatalf("read %d templates; expected the widened set", len(doc.Templates))
	}
	for _, tpl := range doc.Templates {
		got := approvals.MatchPending(tpl, 1)
		if isNegative(tpl) {
			if got == approvals.Yes {
				t.Errorf("negative template %q reads as Yes", tpl)
			}
			continue
		}
		if got != approvals.Yes {
			t.Errorf("affirmative template %q reads as %v under MatchPending(…, 1): on voice that would DENY the envelope", tpl, got)
		}
	}
}
