package cli

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"water/internal/runtime"
	"water/internal/voice"
)

// TestAskEventHandlerIgnoresUnknownKind is the direct, deterministic proof
// behind "ask ignores the unknown handoff event kind": askEventHandler.handle
// is the exact function client.Turn's onEvent callback is bound to, so
// feeding it a crafted event sequence containing an unrecognized Kind (e.g.
// a future "handoff" event neither runtime nor this switch defines yet)
// must not set an error, and the turn must still complete normally (a
// "done" event still prints).
func TestAskEventHandlerIgnoresUnknownKind(t *testing.T) {
	var out bytes.Buffer
	h := &askEventHandler{ctx: context.Background(), speak: false, out: &out}

	events := []runtime.Event{
		{Kind: runtime.EventAck},
		{Kind: runtime.EventDelta, Text: "hello "},
		{Kind: runtime.EventKind("handoff"), Text: "handing off to voice"}, // unknown kind
		{Kind: runtime.EventDelta, Text: "world"},
		{Kind: runtime.EventDone},
	}
	for _, e := range events {
		h.handle(e)
	}
	if h.err != nil {
		t.Fatalf("handle() set an error on an unknown event kind: %v", h.err)
	}
	if got := out.String(); !strings.Contains(got, "hello world") {
		t.Fatalf("output = %q, want the deltas around the unknown event to still print (turn completed normally)", got)
	}
}

// TestAskEventHandlerStillReportsRealErrors: the tolerance for unknown
// kinds must not swallow a genuine EventError.
func TestAskEventHandlerStillReportsRealErrors(t *testing.T) {
	var out bytes.Buffer
	h := &askEventHandler{ctx: context.Background(), out: &out}
	h.handle(runtime.Event{Kind: runtime.EventKind("handoff")})
	h.handle(runtime.Event{Kind: runtime.EventError, Error: "boom"})
	if h.err == nil || h.err.Error() != "boom" {
		t.Fatalf("err = %v, want \"boom\"", h.err)
	}
}

// TestDaemonClientTurnToleratesUnknownEventKind feeds a crafted NDJSON
// stream containing a "handoff"-kind event line through daemonClient.Turn's
// real parser (bufio.Scanner + json.Unmarshal into runtime.Event, exactly
// as a live daemon's response body would arrive), confirming the whole
// pipe — not just the switch above — doesn't error and delivers every
// event, unknown kind included, in order.
func TestDaemonClientTurnToleratesUnknownEventKind(t *testing.T) {
	body := strings.Join([]string{
		`{"kind":"ack"}`,
		`{"kind":"delta","text":"hi"}`,
		`{"kind":"handoff","text":"switching channels"}`,
		`{"kind":"done"}`,
	}, "\n") + "\n"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/turns" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	c := testDaemonClientAt(srv.Listener.Addr().String())
	var kinds []string
	err := c.Turn(context.Background(), "cli", "hello", false, func(e runtime.Event) {
		kinds = append(kinds, string(e.Kind))
	})
	if err != nil {
		t.Fatalf("Turn returned an error on an unknown event kind: %v", err)
	}
	want := []string{"ack", "delta", "handoff", "done"}
	if len(kinds) != len(want) {
		t.Fatalf("kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("kinds = %v, want %v", kinds, want)
		}
	}
}

// testDaemonClientAt builds a *daemonClient that dials a plain TCP address
// (an httptest server) instead of a Unix socket — the same
// "http://water"+path URL shape, minus newDaemonClient's socket dialer —
// so the NDJSON-parsing path can be tested without a real daemon or token
// file.
func testDaemonClientAt(addr string) *daemonClient {
	return &daemonClient{
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "tcp", addr)
				},
			},
		},
	}
}

// TestApplyVoiceProfile: a non-empty tts.voice/rate_wpm overrides an
// already-resolved voice.OS speaker; an empty tts.voice leaves the
// voice.ceo_voice-based value replySpeaker already resolved untouched
// (the documented fallback).
func TestApplyVoiceProfile(t *testing.T) {
	osv := voice.NewOS()
	osv.SetVoice("Jamie (Premium)")
	osv.SetRate(172)

	applyVoiceProfile(osv, VoiceProfileResult{})
	if osv.Voice() != "Jamie (Premium)" || osv.Rate() != 172 {
		t.Fatalf("an empty profile must leave the resolved voice/rate alone; got voice=%q rate=%d", osv.Voice(), osv.Rate())
	}

	profile := VoiceProfileResult{}
	profile.TTS.Voice = "Zoe (Premium)"
	profile.TTS.RateWPM = 200
	applyVoiceProfile(osv, profile)
	if osv.Voice() != "Zoe (Premium)" {
		t.Fatalf("voice = %q, want the profile's override", osv.Voice())
	}
	if osv.Rate() != 200 {
		t.Fatalf("rate = %d, want the profile's override", osv.Rate())
	}

	// A non-OS provider (nil here stands in for e.g. the OpenAI provider)
	// must not panic and must simply be left alone.
	applyVoiceProfile(nil, profile)
}
