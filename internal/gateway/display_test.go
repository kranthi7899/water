package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/connectors"
	"water/internal/connectors/display"
	"water/internal/gate"
	"water/internal/runtime"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

const displayTestManifest = `
id: test
name: Test twin
usage: {window: 1h, model_calls: 50, auto_model_calls: 10}
connectors:
  - name: display
    functions:
      - {name: show, level: R, rate: {max: 30, per: 1h}}
`

// newDisplayHarness is newHarness over the real display connector, so
// display.show goes through the real gate and tool bridge.
func newDisplayHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log, err := audit.Open(filepath.Join(dir, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	q := approvals.NewQueue(st, log)
	reg, err := connectors.NewRegistry(display.New())
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(displayTestManifest))
	if err != nil {
		t.Fatal(err)
	}
	v := vault.NewMemory()
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: v, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	fb := backend.NewFake("fake")
	clients, err := LoadClients(filepath.Join(dir, "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := clients.EnsureCLI()
	if err != nil {
		t.Fatal(err)
	}
	d := New(Config{Manifest: m, Store: st, Audit: log, Approvals: q, Gate: g, Registry: reg, Backend: fb, Clients: clients,
		SocketPath: "unused-in-http-tests.sock", Nervous: testNervous(t, m, st)})
	srv := httptest.NewServer(d.Mux())
	t.Cleanup(srv.Close)
	return &harness{d: d, srv: srv, token: tok, st: st, log: log, q: q, fake: fb, vault: v, dir: dir}
}

// TestDisplayShowEmitsOneArtifact: a successful display.show call during a
// turn streams exactly one display artifact between its tool_start
// ("Showing you this") and tool_end, carrying the call's title and body.
func TestDisplayShowEmitsOneArtifact(t *testing.T) {
	h := newDisplayHarness(t)
	rec, _ := activeSink(h, "task_a")
	out := h.invokeAsModel(t, gate.P0, gate.Clean, "display.show", map[string]any{"title": "Runway", "body": "Cash: $4.2M\nBurn: $300k/mo\nRunway: 14 months"})
	if out["status"] != "ok" {
		t.Fatalf("out = %+v, want ok", out)
	}
	events := rec.all()
	if len(events) != 3 || events[0].Kind != runtime.EventToolStart || events[1].Kind != runtime.EventArtifact || events[2].Kind != runtime.EventToolEnd {
		t.Fatalf("events = %+v, want tool_start, artifact, tool_end", events)
	}
	if events[0].Label != "Showing you this" || events[2].Status != runtime.StepOK {
		t.Fatalf("step = %+v / %+v", events[0], events[2])
	}
	e := events[1]
	if e.StepID != events[0].StepID || e.Tool != "display.show" {
		t.Fatalf("artifact event = %+v", e)
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"kind":"artifact","step_id":"` + e.StepID + `","tool":"display.show","artifact":{"type":"display","title":"Runway","body":"Cash: $4.2M\nBurn: $300k/mo\nRunway: 14 months"}}`
	if string(b) != want {
		t.Fatalf("wire = %s\nwant   %s", b, want)
	}
}

// TestDisplayShowInvalidArgsEmitNothing: a missing or oversized title or
// body is refused (denied, nothing ran) and no artifact is sent.
func TestDisplayShowInvalidArgsEmitNothing(t *testing.T) {
	for name, args := range map[string]map[string]any{
		"missing title":  {"body": "b"},
		"missing body":   {"title": "t"},
		"blank body":     {"title": "t", "body": "   "},
		"title too long": {"title": strings.Repeat("x", display.MaxTitle+1), "body": "b"},
		"body too long":  {"title": "t", "body": strings.Repeat("x", display.MaxBody+1)},
		"extra argument": {"title": "t", "body": "b", "to": "dana@acme.com"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newDisplayHarness(t)
			rec, _ := activeSink(h, "task_a")
			if out := h.invokeAsModel(t, gate.P0, gate.Clean, "display.show", args); out["status"] != "denied" {
				t.Fatalf("out = %+v, want denied", out)
			}
			events := rec.all()
			if a := artifactEvents(events); len(a) != 0 {
				t.Fatalf("artifact emitted: %+v", a)
			}
			if steps := stepEvents(events); len(steps) != 2 || steps[1].Status != runtime.StepDenied {
				t.Fatalf("steps = %+v", steps)
			}
		})
	}
}

// TestDisplayShowNoActiveTurn: with no active turn the call still answers
// ok, and no stream (not even a registered, idle one) gets anything.
func TestDisplayShowNoActiveTurn(t *testing.T) {
	h := newDisplayHarness(t)
	idle := &stepRecorder{}
	h.d.registerSink("task_waiting", &turnSink{write: idle.write})
	if out := h.invokeAsModel(t, gate.P0, gate.Clean, "display.show", map[string]any{"title": "t", "body": "b"}); out["status"] != "ok" {
		t.Fatalf("out = %+v, want ok", out)
	}
	if got := idle.all(); len(got) != 0 {
		t.Fatalf("a non-active stream got %+v", got)
	}
}

// TestDisplayArtifactCaps: the artifact builder trims and caps title and
// body to display.MaxTitle / display.MaxBody characters on its own (so a
// stray path can never flood a stream), and yields nothing when either is
// missing or blank.
func TestDisplayArtifactCaps(t *testing.T) {
	a := turnArtifact("display.show", map[string]any{"title": " " + strings.Repeat("é", display.MaxTitle+5), "body": strings.Repeat("ü", display.MaxBody+5) + " "}, nil)
	if a == nil || a.Type != runtime.ArtifactDisplay {
		t.Fatalf("artifact = %+v", a)
	}
	if n := utf8.RuneCountInString(a.Title); n != display.MaxTitle || !utf8.ValidString(a.Title) {
		t.Fatalf("title = %d runes", n)
	}
	if n := utf8.RuneCountInString(a.Body); n != display.MaxBody || !utf8.ValidString(a.Body) {
		t.Fatalf("body = %d runes", n)
	}
	if a.To != nil || a.Cc != nil || a.Subject != "" {
		t.Fatalf("display artifact carries draft fields: %+v", a)
	}
	for _, args := range []map[string]any{nil, {"title": "t"}, {"body": "b"}, {"title": " ", "body": "b"}, {"title": 1, "body": "b"}} {
		if a := turnArtifact("display.show", args, nil); a != nil {
			t.Fatalf("args %v: artifact %+v, want none", args, a)
		}
	}
	// Drafts still go through the same builder.
	if a := turnArtifact("gmail.draft_message", map[string]any{"subject": "S"}, nil); a == nil || a.Type != runtime.ArtifactEmailDraft {
		t.Fatalf("draft artifact = %+v", a)
	}
	if turnArtifact("gcal.list_events", map[string]any{"title": "t", "body": "b"}, nil) != nil {
		t.Fatal("non-artifact function produced an artifact")
	}
}

// TestVoiceTurnDisplayStreamsArtifact: a voice main-path turn whose model
// calls display.show streams tool_start, artifact, tool_end before done,
// and the old-client event shape still decodes it.
func TestVoiceTurnDisplayStreamsArtifact(t *testing.T) {
	h := newDisplayHarness(t)
	h.fake.Reply = func(req backend.Request) string {
		body, _ := json.Marshal(map[string]any{"function": "display.show", "args": map[string]any{"title": "Today", "body": "1. Board prep\n2. Hiring sync"}})
		r, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/v1/tools/invoke", bytes.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+req.Tools.TwinToken)
		resp, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Error(err)
			return "failed"
		}
		resp.Body.Close()
		return "Two things today; they're on screen."
	}
	events := readEvents(t, h.post(t, "/v1/turns", `{"channel":"voice","prompt":"what's on today"}`, h.token))
	var arts []runtime.Event
	for _, e := range events {
		if e.Kind == runtime.EventArtifact {
			arts = append(arts, e)
		}
	}
	if len(arts) != 1 || arts[0].Artifact == nil || arts[0].Artifact.Type != "display" || arts[0].Artifact.Title != "Today" ||
		arts[0].Artifact.Body != "1. Board prep\n2. Hiring sync" {
		t.Fatalf("artifacts = %+v (all %+v)", arts, events)
	}
	b, _ := json.Marshal(arts[0])
	var old struct {
		Kind string `json:"kind"`
		Text string `json:"text,omitempty"`
	}
	if err := json.Unmarshal(b, &old); err != nil || old.Kind != "artifact" {
		t.Fatalf("old client decode = %+v %v", old, err)
	}
}
