# Voice

Water treats speech as a renderer for an already-completed text reply. The model does not receive
audio, and voice never changes persona, memory, tool permissions, or orchestration.

## Local default

`voice.provider: os` uses macOS `say` (or `espeak` / `spd-say` on Linux) with no API charge:

```sh
water --voice chat ceo
```

In chat, `○ >` means speech is available but off; `♪ >` means replies will be spoken. Toggle with
`/voice on`, `/voice off`, `/voice status`, or Ctrl+B.

## Expressive role voices (optional and metered)

OpenAI speech produces a more natural, role-specific speaking voice. It is a separate paid API surface,
not a benefit of a Claude, Codex, or ChatGPT subscription. Enable it explicitly:

```sh
export OPENAI_API_KEY='...'
water config set voice.provider openai
water config set voice.allow_metered true
water config set voice.model gpt-4o-mini-tts
water --voice chat ceo
```

Defaults are CEO `marin`, COO `cedar`, CTO `ash`, and Design `coral`. Override a role’s built-in voice
identifier if desired:

```sh
water config set voice.design_voice shimmer
```

`water doctor` reports whether the expressive provider is ready. If API use is not explicitly enabled,
Water refuses to speak through it rather than silently billing an account.

This is expressive text-to-speech, not a cloned human voice and not a live Sora/Realtime conversation.
Custom voices require the provider’s consent process and account eligibility. Speech-to-text is currently
unavailable; type into Water as normal.
