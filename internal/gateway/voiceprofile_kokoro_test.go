package gateway

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"water/internal/nervous"
	"water/internal/nervous/intents"
	"water/internal/nervous/reflex"
	"water/internal/nervous/render"
	"water/internal/twins"
)

// kokoroStyleYAML is a minimal valid style.yaml with voice.tts.kokoro_voice
// set (Slice V's V-voice sub-slice, V-7).
const kokoroStyleYAML = `
tone: "Calm, brief, plain."
max_chars: {cli: 2000, text-bar: 600, voice: 280}
max_list_items: {cli: 20, text-bar: 8, voice: 3}
confirmation: "Got it."
prompt_block: "Be brief."
responses: {}
voice:
  name: Water
  tone: "Calm, brief, plain."
  handoff: ["One moment."]
  errors:
    generic: "I can't answer that right now."
    timeout: "That's taking too long."
    tap_required: "That needs a tap."
    readback_stale: "That changed."
    nothing_pending: "Nothing pending."
  banned_phrases: []
  max_sentence_chars: 200
  tts: {voice: "Samantha", rate_wpm: 185, kokoro_voice: "af_heart"}
`

// noKokoroStyleYAML is the same style.yaml with kokoro_voice omitted
// entirely, proving the field is truly optional end to end.
const noKokoroStyleYAML = `
tone: "Calm, brief, plain."
max_chars: {cli: 2000, text-bar: 600, voice: 280}
max_list_items: {cli: 20, text-bar: 8, voice: 3}
confirmation: "Got it."
prompt_block: "Be brief."
responses: {}
voice:
  name: Water
  tone: "Calm, brief, plain."
  handoff: ["One moment."]
  errors:
    generic: "I can't answer that right now."
    timeout: "That's taking too long."
    tap_required: "That needs a tap."
    readback_stale: "That changed."
    nothing_pending: "Nothing pending."
  banned_phrases: []
  max_sentence_chars: 200
  tts: {voice: "Samantha", rate_wpm: 185}
`

// newVoiceProfileDaemon builds the smallest possible *Daemon that
// handleVoiceProfile can serve from: a real *nervous.Nervous carrying the
// given style.yaml, and nothing else handleVoiceProfile itself touches.
func newVoiceProfileDaemon(t *testing.T, styleYAML string) *Daemon {
	t.Helper()
	m, err := twins.Parse([]byte(testManifest))
	if err != nil {
		t.Fatal(err)
	}
	intentsFS := fstest.MapFS{"twins/test/intents/_shared.yaml": &fstest.MapFile{Data: []byte("skip_words: []\n")}}
	reg, err := intents.LoadRegistry(intentsFS, m, intents.Functions{Read: reflex.Specs()}, intents.LoadOptions{})
	if err != nil {
		t.Fatalf("intents.LoadRegistry: %v", err)
	}
	styleFS := fstest.MapFS{"twins/test/style.yaml": &fstest.MapFile{Data: []byte(styleYAML)}}
	style, err := render.LoadStyle(styleFS, "test")
	if err != nil {
		t.Fatalf("render.LoadStyle: %v", err)
	}
	nvCfg := nervous.DefaultConfig()
	nvCfg.Registry = func() *intents.Registry { return reg }
	nvCfg.Style = style
	nv, err := nervous.New(nvCfg)
	if err != nil {
		t.Fatalf("nervous.New: %v", err)
	}
	return New(Config{Nervous: nv})
}

// getVoiceProfile calls handleVoiceProfile directly (no HTTP server needed:
// the handler only reads d.cfg.Nervous) and decodes its JSON body.
func getVoiceProfile(t *testing.T, d *Daemon) map[string]any {
	t.Helper()
	req := httptest.NewRequest("GET", "/v1/voice/profile", nil)
	rec := httptest.NewRecorder()
	d.handleVoiceProfile(rec, req)
	if rec.Code != 200 {
		t.Fatalf("handleVoiceProfile status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode /v1/voice/profile response: %v", err)
	}
	return body
}

// TestHandleVoiceProfileIncludesKokoroVoiceWhenSet proves style.yaml's
// voice.tts.kokoro_voice reaches GET /v1/voice/profile's JSON response
// (Slice V's V-voice sub-slice, V-7).
func TestHandleVoiceProfileIncludesKokoroVoiceWhenSet(t *testing.T) {
	d := newVoiceProfileDaemon(t, kokoroStyleYAML)
	body := getVoiceProfile(t, d)
	tts, ok := body["tts"].(map[string]any)
	if !ok {
		t.Fatalf("tts field missing or wrong shape: %+v", body)
	}
	if got := tts["kokoro_voice"]; got != "af_heart" {
		t.Fatalf("tts.kokoro_voice = %v, want af_heart", got)
	}
}

// TestHandleVoiceProfileOmitsKokoroVoiceWhenUnset proves a style.yaml with
// no kokoro_voice still validates and answers, and that the response omits
// the key entirely rather than emitting an empty string.
func TestHandleVoiceProfileOmitsKokoroVoiceWhenUnset(t *testing.T) {
	d := newVoiceProfileDaemon(t, noKokoroStyleYAML)
	body := getVoiceProfile(t, d)
	tts, ok := body["tts"].(map[string]any)
	if !ok {
		t.Fatalf("tts field missing or wrong shape: %+v", body)
	}
	if _, present := tts["kokoro_voice"]; present {
		t.Fatalf("tts.kokoro_voice present = %+v, want key omitted entirely", tts)
	}
}
