package voice

import "water/internal/nervous/speak"

// Speakable flattens markdown into prose a TTS engine can read naturally:
// code blocks become a short spoken note, markup characters are dropped, and
// each list item or table row ends as its own sentence so the engine pauses.
//
// The implementation moved to internal/nervous/speak.FlattenMarkdown (Slice
// R task R-13), which the one-voice contract's speak.Speakable (bare URLs,
// emoji, time/date normalization, list/length capping) builds on for the
// nervous facade's voice-channel output. This delegate keeps this package's
// existing, narrower behaviour byte-for-byte unchanged for its own callers
// (internal/decisions and this package's own tests).
func Speakable(text string) string {
	return speak.FlattenMarkdown(text)
}
