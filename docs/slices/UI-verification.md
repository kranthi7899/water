# UI on-device verification, owed to the owner

This file tracks on-device checks that no agent can perform: they require a
real microphone, real audio output, and the owner's actual running
Water.app/daemon, none of which an agent session is allowed to touch (see
CLAUDE.md and the Phase 1a incident logged in `docs/EVOLUTION_PLAN.md`).
Created by Slice UI Phase 6 (`docs/slices/UI.md`, "On-device verification
owed" list), which named these four items verbatim. Nothing below has been
attempted, simulated, or faked by any agent — that would defeat the point of
tracking it here.

## Owed from Phase 6 (voice and the Activity HUD, restated)

1. **Banner, tap and second-tap reuse.** Confirm the notification banner,
   a first tap on it, and a second tap on the same (still-open) item all
   behave as intended on the owner's own Mac and notification settings.

2. **Glass-tab Approve against spoken "yes → confirm send."** Confirm that
   tapping Approve in the glass tab and completing Slice W's two-step
   spoken confirm ("yes" then "confirm send") on a real `gmail.send_message`
   envelope both resolve the same way on-device — the automated coverage
   (`internal/nervous/voiceapprove_bind_test.go`,
   `TestVoiceTwoStepConfirmAndClickApproveResolveTheSameWay`, added this
   phase) proves the two paths share one underlying decision mechanism in
   code, but never touches a real microphone, real Kokoro audio, or the
   real glass-tab UI.

3. **Kokoro first-audio latency.** Measure real first-audio latency for
   Kokoro (via FluidAudio) on-device; no agent session may run audio
   synthesis or measure real playback latency.

4. **The first hold after launch.** Confirm the first push-to-talk hold
   right after a fresh launch of Water.app behaves correctly (model
   loading, warm-up stalls, first transcript) on a real launch.

## Status

All four are still owed. None has been performed by any agent, and none
should be — they require the owner's own hands-on testing with a real
microphone, real audio output, and the owner's actual running app.
