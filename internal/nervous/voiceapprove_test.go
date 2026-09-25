package nervous

import (
	"testing"

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
			name: "rule4 gmail.send_message taps even at low risk (allowlist exclusion)",
			env: approvals.Envelope{
				Origin: "p0", Risk: "low", Action: "gmail.send_message",
				Payload: map[string]any{"to": []string{"a@acme.com"}},
			},
			domains: internal, wantTier: TapRequired, wantReason: "not voice-eligible",
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
