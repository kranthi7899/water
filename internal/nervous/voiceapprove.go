package nervous

import (
	"strings"
	"time"

	"water/internal/approvals"
)

// VoiceTier is VoiceApprovalTier's verdict: whether a spoken "yes" may
// itself decide an envelope, or whether it always needs a tap instead.
type VoiceTier int

const (
	// VoiceYes means a spoken "yes", bound to this exact envelope and
	// payload hash within the window, may decide it directly.
	VoiceYes VoiceTier = iota
	// TapRequired means a spoken "yes" never decides this envelope, no
	// matter how it's phrased: the CEO must tap to confirm instead. A
	// spoken "no" is always allowed regardless of tier (denying is safe).
	TapRequired
)

// DefaultVoiceApproveWindow is router.voice_approve.window_seconds's
// default (Design §17).
const DefaultVoiceApproveWindow = 60 * time.Second

// VoiceApproveConfig gates and configures the voice channel's yes/no
// binding (router.voice_approve.*, code default off). Config.Approver must
// also be set on Nervous.Config for Enabled to have any effect.
type VoiceApproveConfig struct {
	Enabled bool
	// Window bounds how long after a read-back a bare yes/no on the same
	// channel still binds to it (default DefaultVoiceApproveWindow).
	Window time.Duration
	// InternalDomains lists the domains a recipient address may belong to
	// without requiring a tap (rule 5). An empty list means every address
	// is external — fail closed, never fail open.
	InternalDomains []string
}

// voiceEligibleActions is the only actions a spoken "yes" may ever approve
// (default deny): everything else, including gmail.send_message and any
// future money- or public-post-shaped function, always needs a tap. This
// list is deliberately independent of the manifest — a new function is
// Tap-required by default until someone deliberately adds it here.
var voiceEligibleActions = map[string]bool{
	"gcal.create_event":   true,
	"gcal.move_event":     true,
	"gmail.draft_message": true,
}

// recipientPayloadKeys are the payload fields VoiceApprovalTier scans for
// addresses, alongside the envelope's own Recipient field.
var recipientPayloadKeys = []string{"to", "cc", "bcc", "attendees"}

// VoiceApprovalTier maps an envelope to whether a spoken "yes" may decide it
// (Design §13). Rules are evaluated in order and the first match wins:
//
//  1. e.Origin is auto-mode ("p2"): Tap ("auto-mode origin").
//  2. e.Risk is "high": Tap ("declared high risk").
//  3. e.Risk is anything other than exactly "low" or "medium" (empty or
//     unrecognized): Tap ("unrated").
//  4. e.Action is not in voiceEligibleActions: Tap ("not voice-eligible").
//  5. Any address in e.Recipient, or in the payload's to/cc/bcc/attendees
//     fields, has a domain not in internalDomains (an empty internalDomains
//     list means every address is external): Tap ("external recipient").
//  6. Otherwise: VoiceYes.
//
// A spoken "no" is always allowed, at every tier — this function is never
// consulted for one.
func VoiceApprovalTier(e approvals.Envelope, internalDomains []string) (VoiceTier, string) {
	// "p2" mirrors gate.P2's underlying string exactly (internal/gate.Origin
	// = "p2"); this package never imports internal/gate (Design §1's
	// dependency-direction rule), so the literal is used directly instead.
	if e.Origin == "p2" {
		return TapRequired, "auto-mode origin"
	}
	if e.Risk == "high" {
		return TapRequired, "declared high risk"
	}
	if e.Risk != "low" && e.Risk != "medium" {
		return TapRequired, "unrated"
	}
	if !voiceEligibleActions[e.Action] {
		return TapRequired, "not voice-eligible"
	}
	internal := make(map[string]bool, len(internalDomains))
	for _, d := range internalDomains {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			internal[d] = true
		}
	}
	var addrs []string
	addrs = append(addrs, addressesOf(e.Recipient)...)
	for _, key := range recipientPayloadKeys {
		addrs = append(addrs, addressesOf(e.Payload[key])...)
	}
	for _, a := range addrs {
		if d := domainOf(a); d == "" || !internal[d] {
			return TapRequired, "external recipient"
		}
	}
	return VoiceYes, ""
}

// addressesOf reads v defensively: the payload is map[string]any decoded
// from JSON, so a recipient field may be a bare string or a list of them
// ([]string built by a proposer, []any decoded from stored JSON).
func addressesOf(v any) []string {
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// domainOf returns addr's lowercased domain, or "" when addr has no "@"
// (which VoiceApprovalTier then treats as external, never as a match for
// any configured internal domain).
func domainOf(addr string) string {
	i := strings.LastIndex(addr, "@")
	if i < 0 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(addr[i+1:]))
}
