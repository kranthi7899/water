package render

import "sync"

// defaultStyleYAML is the compiled-in fallback for a twin with no
// style.yaml of its own. It goes through the exact same parseStyle path
// (decode + validate) as a real file, so it can never silently drift out
// of the schema the rest of this package enforces.
const defaultStyleYAML = `
tone: "Calm, brief, plain. Facts first, no filler."
max_chars: {cli: 2000, text-bar: 600, voice: 280}
max_list_items: {cli: 20, text-bar: 8, voice: 3}
confirmation: "Got it."
prompt_block: >
  Answer in a calm, brief, plain voice. Facts first. No filler, no
  apologies, no hedging. Never say "As an AI" or similar. On voice, avoid
  markdown and long lists.
responses: {}
voice:
  name: Water
  tone: "Calm, brief, plain. Facts first. No filler, no apologies, no hedging."
  handoff: ["One moment.", "Let me check.", "On it."]
  errors:
    generic: "I can't answer that right now."
    timeout: "That's taking too long. Try again in a moment."
    tap_required: "That one needs a tap to confirm."
    readback_stale: "That changed. Here it is again."
    nothing_pending: "Nothing is waiting for approval."
  banned_phrases: ["as an ai", "as a language model", "great question", "i'd be happy to", "certainly!", "absolutely!", "i hope this helps"]
  max_sentence_chars: 200
  tts: {voice: "", rate_wpm: 185}
`

var (
	defaultStyleOnce sync.Once
	defaultStyleVal  *Style
)

// DefaultStyle returns the compiled-in fallback style. It always succeeds:
// a failure here would mean defaultStyleYAML itself is broken, which is a
// programming error caught by TestDefaultStyleValid, not a runtime
// condition callers need to handle.
func DefaultStyle() *Style {
	defaultStyleOnce.Do(func() {
		s, err := parseStyle([]byte(defaultStyleYAML))
		if err != nil {
			panic("render: built-in default style.yaml is invalid: " + err.Error())
		}
		defaultStyleVal = s
	})
	return defaultStyleVal
}
