package gateway

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/connectors"
	"water/internal/connectors/fake"
	"water/internal/gate"
	"water/internal/nervous"
	"water/internal/nervous/intents"
	"water/internal/nervous/reflex"
	"water/internal/nervous/render"
	"water/internal/runtime"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

// routingSharedYAML is the minimal _shared.yaml a real (non-empty) intent
// registry needs (Design §5.5's file is required whenever the intents
// directory exists at all).
const routingSharedYAML = "skip_words: [please]\n"

// routingStatusIntentYAML declares one real, reflex-eligible read intent
// (status.overview: no slots, no external data needed) so these tests can
// prove Tier 0 answers something real end to end over HTTP, distinct from
// the other gateway tests' deliberately empty registries.
const routingStatusIntentYAML = `
id: status.overview
description: What is running and which connectors are synced
function: status.overview
templates: ["status", "what's your status"]
reflex_eligible: true
escalate_if: [slot_unresolved, ambiguous_match]
tests:
  - {utterance: "status", intent: status.overview}
  - {utterance: "hello there", intent: none}
`

// newRoutingHarness is newHarness's sibling, built with a real one-intent
// registry (rather than newHarness's empty one) so a Tier-0-answerable
// turn is actually possible over real HTTP.
func newRoutingHarness(t *testing.T) *harness {
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

	mail := fake.NewMail()
	nt := &notes{}
	sa := newSlowAct()
	reg, err := connectors.NewRegistry(mail, nt, sa)
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(testManifest))
	if err != nil {
		t.Fatal(err)
	}
	v := vault.NewMemory()
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: v, Store: st})
	if err != nil {
		t.Fatal(err)
	}
	fb := backend.NewFake("fake")
	fb.Reply = func(backend.Request) string { return "main path reply" }
	clients, err := LoadClients(filepath.Join(dir, "clients.json"))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := clients.EnsureCLI()
	if err != nil {
		t.Fatal(err)
	}

	fsys := fstest.MapFS{
		"twins/test/intents/_shared.yaml": &fstest.MapFile{Data: []byte(routingSharedYAML)},
		"twins/test/intents/status.yaml":  &fstest.MapFile{Data: []byte(routingStatusIntentYAML)},
	}
	intentsReg, err := intents.LoadRegistry(fsys, m, intents.Functions{Read: reflex.Specs()}, intents.LoadOptions{})
	if err != nil {
		t.Fatalf("intents.LoadRegistry: %v", err)
	}
	nvCfg := nervous.DefaultConfig()
	nvCfg.Registry = func() *intents.Registry { return intentsReg }
	nvCfg.Style = render.DefaultStyle()
	nvCfg.Store = st
	nv, err := nervous.New(nvCfg)
	if err != nil {
		t.Fatalf("nervous.New: %v", err)
	}

	d := New(Config{Manifest: m, Store: st, Audit: log, Approvals: q, Gate: g, Registry: reg, Backend: fb, Clients: clients, SocketPath: "unused-in-http-tests.sock", Nervous: nv})
	srv := httptest.NewServer(d.Mux())
	t.Cleanup(srv.Close)
	return &harness{d: d, srv: srv, token: tok, st: st, log: log, q: q, fake: fb, mail: mail, dir: dir}
}

// TestRoutingTier0AnswerMakesNoProviderCall proves a real Tier 0 intent
// answers over real HTTP without ever calling the backend, and writes one
// route_log row.
func TestRoutingTier0AnswerMakesNoProviderCall(t *testing.T) {
	h := newRoutingHarness(t)
	resp := h.post(t, "/v1/turns", `{"channel":"cli","prompt":"status"}`, h.token)
	events := readEvents(t, resp)
	if len(events) == 0 || events[len(events)-1].Kind != runtime.EventDone {
		t.Fatalf("events = %+v", events)
	}
	if n := h.fake.Calls(); n != 0 {
		t.Fatalf("provider calls = %d, want 0 (status.overview should answer from Tier 0)", n)
	}
	rows, err := h.st.ListRoutes(context.Background(), time.Now().Add(-time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("route_log rows = %d, want 1", len(rows))
	}
	if rows[0].AnsweredBy != "t0" {
		t.Fatalf("answered_by = %q, want t0", rows[0].AnsweredBy)
	}
}

// TestRoutingTier0MissStreamsFromMainPath: an utterance no intent matches
// escalates to the main path and streams the fake backend's reply.
func TestRoutingTier0MissStreamsFromMainPath(t *testing.T) {
	h := newRoutingHarness(t)
	resp := h.post(t, "/v1/turns", `{"channel":"cli","prompt":"tell me a story"}`, h.token)
	events := readEvents(t, resp)
	if len(events) == 0 || events[len(events)-1].Kind != runtime.EventDone {
		t.Fatalf("events = %+v", events)
	}
	if n := h.fake.Calls(); n != 1 {
		t.Fatalf("provider calls = %d, want 1", n)
	}
}

// TestRoutingMeetingContextNeverFeedsTier0: a meeting_id's transcript
// content must never itself satisfy a Tier 0 template match — only the
// CEO's own raw prompt is matched — while the meeting taint still
// escalates exactly as it does on the main path.
func TestRoutingMeetingContextNeverFeedsTier0(t *testing.T) {
	h := newRoutingHarness(t)
	id := h.startMeeting(t, `{}`)
	if code := h.status(t, "/v1/meetings/"+id+"/segments", `{"channel":"mic","text":"status"}`); code != 200 {
		t.Fatalf("segment: status = %d", code)
	}
	// The CEO's own prompt ("status") matches Tier 0 regardless of the
	// meeting transcript containing the same word — Text is what Tier 0
	// sees, not Context.
	resp := h.post(t, "/v1/turns", `{"channel":"cli","prompt":"status","meeting_id":"`+id+`"}`, h.token)
	events := readEvents(t, resp)
	if len(events) == 0 || events[len(events)-1].Kind != runtime.EventDone {
		t.Fatalf("events = %+v", events)
	}
	if n := h.fake.Calls(); n != 0 {
		t.Fatalf("provider calls = %d, want 0 (status still answers from Tier 0 even with a meeting_id)", n)
	}
	if got := h.sessionTaint(t); got != gate.Tainted {
		t.Fatalf("taint = %v, want tainted (a live meeting session is untrusted by existing, regardless of which tier answered)", got)
	}
}

// TestRouterHealthAndReportEndpoints: GET /v1/router and GET
// /v1/route/report return sane, decodable JSON.
func TestRouterHealthAndReportEndpoints(t *testing.T) {
	h := newRoutingHarness(t)
	readEvents(t, h.post(t, "/v1/turns", `{"channel":"cli","prompt":"status"}`, h.token))

	resp := h.get(t, "/v1/router", h.token)
	var health map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("decode /v1/router: %v", err)
	}
	resp.Body.Close()
	if _, ok := health["tier0"]; !ok {
		t.Fatalf("/v1/router response missing tier0: %+v", health)
	}

	resp = h.get(t, "/v1/route/report", h.token)
	var report nervous.Report
	if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
		t.Fatalf("decode /v1/route/report: %v", err)
	}
	resp.Body.Close()
	if report.N < 1 {
		t.Fatalf("report.N = %d, want at least 1", report.N)
	}

	resp = h.get(t, "/v1/voice/profile", h.token)
	var profile map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&profile); err != nil {
		t.Fatalf("decode /v1/voice/profile: %v", err)
	}
	resp.Body.Close()
	if _, ok := profile["tts"]; !ok {
		t.Fatalf("/v1/voice/profile response missing tts: %+v", profile)
	}
}
