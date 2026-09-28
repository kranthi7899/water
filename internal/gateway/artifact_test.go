package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/connectors"
	"water/internal/connectors/display"
	"water/internal/gate"
	"water/internal/gate/permit"
	"water/internal/runtime"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

const draftTestManifest = `
id: test
name: Test twin
usage: {window: 1h, model_calls: 50, auto_model_calls: 10}
connectors:
  - name: gmail
    functions:
      - {name: list_messages, level: R}
      - {name: draft_message, level: D}
      - {name: draft_for_review, level: D}
      - {name: send_message, level: A}
`

// fakeGmail stands in for the gmail connector under its real name and
// function ids, with no network: the draft functions just record their
// args. fail makes Invoke refuse (nothing ran); normErr makes Normalize
// fail after the draft was made (executed_with_error).
type fakeGmail struct {
	mu            sync.Mutex
	fail, normErr error
	drafts        int
	// output, when set, replaces the default {"id":"d1"} response for every
	// call -- so a test can make a plain function (list_messages) return a
	// tool result shaped like a future connector's relay: true flag, without
	// needing a real Linear connector to exist yet (docs/slices/UI.md Phase
	// 6, U1-A; linear.create_comment itself is Phase 7/U4 work).
	output json.RawMessage
}

func (*fakeGmail) Name() string                 { return "gmail" }
func (*fakeGmail) Credential() (string, string) { return "", "" }
func (*fakeGmail) Functions() []connectors.Function {
	msg := connectors.Schema{Properties: map[string]connectors.Property{
		"to":      {Type: "array", Items: &connectors.Property{Type: "string"}},
		"cc":      {Type: "array", Items: &connectors.Property{Type: "string"}},
		"subject": {Type: "string"},
		"body":    {Type: "string"},
	}}
	// sendMsg mirrors gmail.go's real sendMessageSchema: send_message's own
	// schema, not shared with draft_message/draft_for_review, declaring the
	// three brand payload-hash fields addBrandPayloadFields adds (real,
	// functionally unused string arguments -- see gmail.go's sendMessageSchema
	// doc comment for why they have to be declared rather than stripped).
	// Kept in sync with the real connector by hand since a fake can't import
	// the real one's unexported var; TestApprovedGmailSendWithBrandFields
	// ActuallyExecutes is what would catch this drifting.
	sendMsg := connectors.Schema{Properties: map[string]connectors.Property{
		"to":               msg.Properties["to"],
		"cc":               msg.Properties["cc"],
		"subject":          msg.Properties["subject"],
		"body":             msg.Properties["body"],
		"template_version": {Type: "string"},
		"signature_hash":   {Type: "string"},
		"asset_hashes":     {Type: "string"},
	}}
	return []connectors.Function{
		{Name: "list_messages", Level: twins.R, Risk: connectors.RiskLow, Activity: "Searching your email"},
		{Name: "draft_message", Level: twins.D, Risk: connectors.RiskLow, Activity: "Drafting an email", Schema: msg},
		{Name: "draft_for_review", Level: twins.D, Risk: connectors.RiskLow, Activity: "Drafting an email for you to review", Schema: msg},
		{Name: "send_message", Level: twins.A, Risk: connectors.RiskHigh, Activity: "Preparing an email to send", Schema: sendMsg},
	}
}
func (g *fakeGmail) Invoke(_ context.Context, p permit.Permit) (json.RawMessage, error) {
	if _, err := p.Open(); err != nil {
		return nil, err
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fail != nil {
		return nil, g.fail
	}
	g.drafts++
	if g.output != nil {
		return g.output, nil
	}
	return json.RawMessage(`{"id":"d1"}`), nil
}
func (g *fakeGmail) Normalize(string, json.RawMessage) ([]store.Record, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return nil, g.normErr
}

// newDraftHarness is newHarness over draftTestManifest and fakeGmail, so
// the real draft function ids go through the real gate and tool bridge.
func newDraftHarness(t *testing.T) (*harness, *fakeGmail) {
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
	gm := &fakeGmail{}
	reg, err := connectors.NewRegistry(gm)
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(draftTestManifest))
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
	return &harness{d: d, srv: srv, token: tok, st: st, log: log, q: q, fake: fb, vault: v, dir: dir}, gm
}

func artifactEvents(events []runtime.Event) []runtime.Event {
	var out []runtime.Event
	for _, e := range events {
		if e.Kind == runtime.EventArtifact {
			out = append(out, e)
		}
	}
	return out
}

// TestDraftCallEmitsOneArtifact: a successful draft_message or
// draft_for_review call during a turn streams exactly one artifact event,
// between its tool_start and tool_end on the same sink, carrying the
// call's own to/cc/subject/body. (The gate validates args against the
// schema first; TestDraftArtifactArgShapes covers the looser shapes.)
func TestDraftCallEmitsOneArtifact(t *testing.T) {
	cases := []struct {
		name     string
		function string
		args     map[string]any
		to, cc   []string
	}{
		{name: "draft_message, lists", function: "gmail.draft_message",
			args: map[string]any{"to": []any{"dana@acme.com", "lee@acme.com"}, "cc": []any{"sam@acme.com"}, "subject": "Q3", "body": "Numbers attached."},
			to:   []string{"dana@acme.com", "lee@acme.com"}, cc: []string{"sam@acme.com"}},
		{name: "draft_for_review, no cc", function: "gmail.draft_for_review",
			args: map[string]any{"to": []any{"dana@acme.com"}, "subject": "Q3", "body": "Numbers attached."},
			to:   []string{"dana@acme.com"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, _ := newDraftHarness(t)
			rec, _ := activeSink(h, "task_a")
			if out := h.invokeAsModel(t, gate.P0, gate.Clean, c.function, c.args); out["status"] != "ok" {
				t.Fatalf("out = %+v, want ok", out)
			}
			events := rec.all()
			if len(events) != 3 || events[0].Kind != runtime.EventToolStart || events[1].Kind != runtime.EventArtifact || events[2].Kind != runtime.EventToolEnd {
				t.Fatalf("events = %+v, want tool_start, artifact, tool_end", events)
			}
			e := events[1]
			if e.StepID != events[0].StepID || e.Tool != c.function || e.Label != "" || e.Status != "" || e.Text != "" {
				t.Fatalf("artifact event = %+v, want only step_id %q, tool %q and artifact", e, events[0].StepID, c.function)
			}
			a := e.Artifact
			if a == nil || a.Type != runtime.ArtifactEmailDraft || a.Subject != "Q3" || a.Body != "Numbers attached." ||
				strings.Join(a.To, ",") != strings.Join(c.to, ",") || strings.Join(a.Cc, ",") != strings.Join(c.cc, ",") {
				t.Fatalf("artifact = %+v, want to %v cc %v", a, c.to, c.cc)
			}
		})
	}
}

// TestNoArtifactUnlessADraftRanCleanly: no artifact event for a draft that
// was refused or failed before running (denied), a draft that ran but whose
// follow-up failed (executed_with_error), a queued call, a non-draft
// function, or a draft made with no active (or an already closed) turn.
// Each case still ends its step as before.
func TestNoArtifactUnlessADraftRanCleanly(t *testing.T) {
	draft := map[string]any{"to": []any{"dana@acme.com"}, "subject": "Q3", "body": "x"}
	cases := []struct {
		name     string
		function string
		args     map[string]any
		setup    func(g *fakeGmail)
		status   runtime.StepStatus
	}{
		{name: "denied", function: "gmail.draft_message", args: draft, setup: func(g *fakeGmail) { g.fail = errors.New("gmail down") }, status: runtime.StepDenied},
		{name: "error", function: "gmail.draft_for_review", args: draft, setup: func(g *fakeGmail) { g.normErr = errors.New("index failed") }, status: runtime.StepError},
		{name: "queued", function: "gmail.send_message", args: draft, status: runtime.StepQueued},
		{name: "non-draft", function: "gmail.list_messages", status: runtime.StepOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, g := newDraftHarness(t)
			if c.setup != nil {
				c.setup(g)
			}
			rec, _ := activeSink(h, "task_a")
			h.invokeAsModel(t, gate.P0, gate.Clean, c.function, c.args)
			events := rec.all()
			if a := artifactEvents(events); len(a) != 0 {
				t.Fatalf("artifact emitted: %+v", a)
			}
			steps := stepEvents(events)
			if len(steps) != 2 || steps[1].Status != c.status {
				t.Fatalf("steps = %+v, want a pair ending %q", steps, c.status)
			}
		})
	}

	t.Run("no active turn", func(t *testing.T) {
		h, g := newDraftHarness(t)
		idle := &stepRecorder{}
		h.d.registerSink("task_waiting", &turnSink{write: idle.write})
		if out := h.invokeAsModel(t, gate.P0, gate.Clean, "gmail.draft_message", draft); out["status"] != "ok" {
			t.Fatalf("out = %+v, want ok", out)
		}
		if got := idle.all(); len(got) != 0 {
			t.Fatalf("a non-active stream got %+v", got)
		}
		if g.drafts != 1 {
			t.Fatalf("drafts = %d, want the call itself unchanged", g.drafts)
		}
	})

	t.Run("closed stream", func(t *testing.T) {
		h, _ := newDraftHarness(t)
		rec, s := activeSink(h, "task_closed")
		s.close()
		h.invokeAsModel(t, gate.P0, gate.Clean, "gmail.draft_message", draft)
		if got := rec.all(); len(got) != 0 {
			t.Fatalf("a closed stream got %+v", got)
		}
	})
}

// TestDraftArtifactArgShapes: to and cc may each be one string or a list;
// empty and non-string entries are skipped.
func TestDraftArtifactArgShapes(t *testing.T) {
	a := draftArtifact("gmail.draft_for_review", map[string]any{"to": "dana@acme.com", "cc": []any{"", 7, "sam@acme.com"}, "subject": "S", "body": "B"})
	if a == nil || strings.Join(a.To, ",") != "dana@acme.com" || strings.Join(a.Cc, ",") != "sam@acme.com" || a.Subject != "S" || a.Body != "B" {
		t.Fatalf("artifact = %+v", a)
	}
	a = draftArtifact("gmail.draft_message", map[string]any{"to": []string{"a@x.com", "b@x.com"}})
	if a == nil || strings.Join(a.To, ",") != "a@x.com,b@x.com" || a.Cc != nil {
		t.Fatalf("artifact = %+v", a)
	}
	if a := draftArtifact("gmail.draft_message", nil); a == nil || a.Type != runtime.ArtifactEmailDraft || a.To != nil {
		t.Fatalf("nil args: artifact = %+v", a)
	}
}

// TestDraftArtifactCaps: strings are cut to artifactMaxBytes on a UTF-8
// boundary and lists to artifactMaxItems; wrong types are dropped, and a
// function that is not a draft yields nothing.
func TestDraftArtifactCaps(t *testing.T) {
	long := strings.Repeat("a", artifactMaxBytes-1) + "é" + "tail" // é straddles the cap
	var many []any
	for i := 0; i < artifactMaxItems+10; i++ {
		many = append(many, "x@acme.com")
	}
	a := draftArtifact("gmail.draft_message", map[string]any{"to": many, "cc": 42, "subject": long, "body": []any{"no"}})
	if a == nil {
		t.Fatal("nil artifact for a draft function")
	}
	if len(a.To) != artifactMaxItems {
		t.Fatalf("to has %d entries, want %d", len(a.To), artifactMaxItems)
	}
	if a.Cc != nil || a.Body != "" {
		t.Fatalf("wrong-typed cc/body not dropped: %+v / %q", a.Cc, a.Body)
	}
	if len(a.Subject) > artifactMaxBytes || !utf8.ValidString(a.Subject) || a.Subject != strings.Repeat("a", artifactMaxBytes-1) {
		t.Fatalf("subject len %d valid %v, want %d valid bytes", len(a.Subject), utf8.ValidString(a.Subject), artifactMaxBytes-1)
	}
	if got := capBytes(strings.Repeat("b", artifactMaxBytes+5), artifactMaxBytes); len(got) != artifactMaxBytes {
		t.Fatalf("ascii cap = %d bytes", len(got))
	}
	for _, fn := range []string{"gmail.send_message", "gmail.list_messages", "fake_mail.draft_reply", ""} {
		if draftArtifact(fn, map[string]any{"to": "x@acme.com"}) != nil {
			t.Fatalf("%q produced an artifact", fn)
		}
	}
}

// TestNoteArtifactRequiresRelayFlag: an ordinary tool result -- absent,
// empty, unparseable, or simply missing "relay":true -- never produces a
// note. A generic result the model might otherwise return (an id, a status)
// is never mistaken for one just because it happens to carry a "title" or
// "body" key.
func TestNoteArtifactRequiresRelayFlag(t *testing.T) {
	cases := []struct {
		name   string
		output json.RawMessage
	}{
		{"nil", nil},
		{"empty", json.RawMessage("")},
		{"not json", json.RawMessage("not json")},
		{"no relay field", json.RawMessage(`{"title":"CRA-3","body":"hi"}`)},
		{"relay false", json.RawMessage(`{"relay":false,"title":"CRA-3","body":"hi"}`)},
		{"relay true but blank title", json.RawMessage(`{"relay":true,"title":"  ","body":"hi"}`)},
		{"relay true but blank body", json.RawMessage(`{"relay":true,"title":"CRA-3","body":" \n "}`)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if a := noteArtifact(c.output); a != nil {
				t.Fatalf("output %s: artifact = %+v, want nil", c.output, a)
			}
		})
	}
}

// TestNoteArtifactEmittedForRelayTrue: a result flagged relay: true builds a
// note artifact carrying its own title/body/source, and Simulated is
// derived from the body's own prefix -- true when it starts with the
// code-added "(simulated) Relayed:" prefix (docs/slices/UI.md U4), false
// otherwise -- regardless of anything else the result claims.
func TestNoteArtifactEmittedForRelayTrue(t *testing.T) {
	a := noteArtifact(json.RawMessage(`{"relay":true,"title":"CRA-3","body":"(simulated) Relayed: Nina, via Slack: it's fixed","source":"linear:CRA-3"}`))
	if a == nil {
		t.Fatal("nil artifact, want a note")
	}
	if a.Type != runtime.ArtifactNote || a.Title != "CRA-3" || a.Source != "linear:CRA-3" {
		t.Fatalf("artifact = %+v", a)
	}
	if a.Body != "(simulated) Relayed: Nina, via Slack: it's fixed" {
		t.Fatalf("body = %q", a.Body)
	}
	if !a.Simulated {
		t.Fatal("Simulated = false, want true: the body carries the code prefix")
	}
	if a.To != nil || a.Cc != nil || a.Subject != "" {
		t.Fatalf("note artifact carries draft fields: %+v", a)
	}

	// A relayed body with no prefix (a real, non-simulated relay, once a
	// live connector exists) is not simulated, even though relay is true.
	real := noteArtifact(json.RawMessage(`{"relay":true,"title":"CRA-3","body":"Nina: fixed for real","source":"linear:CRA-3"}`))
	if real == nil || real.Simulated {
		t.Fatalf("artifact = %+v, want Simulated=false", real)
	}

	// A connector that sets relay:true but forgets/misreports "simulated"
	// itself has no bearing: Simulated is derived from the body, not trusted
	// from an output field the connector might also send.
	untrusted := noteArtifact(json.RawMessage(`{"relay":true,"simulated":false,"title":"t","body":"(simulated) Relayed: x"}`))
	if untrusted == nil || !untrusted.Simulated {
		t.Fatalf("artifact = %+v, want Simulated=true regardless of the output's own claim", untrusted)
	}
}

// TestNoteArtifactCaps: title, body and source are capped the same way a
// draft's fields are (display.MaxTitle runes, artifactMaxBytes bytes).
func TestNoteArtifactCaps(t *testing.T) {
	longBody := strings.Repeat("a", artifactMaxBytes+50)
	longTitle := strings.Repeat("t", display.MaxTitle+50)
	longSource := strings.Repeat("s", artifactMaxBytes+50)
	a := noteArtifact(json.RawMessage(`{"relay":true,"title":"` + longTitle + `","body":"` + longBody + `","source":"` + longSource + `"}`))
	if a == nil {
		t.Fatal("nil artifact")
	}
	if n := utf8.RuneCountInString(a.Title); n != display.MaxTitle {
		t.Fatalf("title = %d runes, want %d", n, display.MaxTitle)
	}
	if len(a.Body) != artifactMaxBytes {
		t.Fatalf("body = %d bytes, want %d", len(a.Body), artifactMaxBytes)
	}
	if len(a.Source) != artifactMaxBytes {
		t.Fatalf("source = %d bytes, want %d", len(a.Source), artifactMaxBytes)
	}
}

// TestTurnArtifactEmitsNoteOverDraftWhenRelayFlagged: turnArtifact checks
// the note path (driven by output) before falling back to the draft path
// (driven by fn); an ordinary draft call's output ({"id":"d1"}, no relay
// field) is completely unaffected.
func TestTurnArtifactEmitsNoteOverDraftWhenRelayFlagged(t *testing.T) {
	a := turnArtifact("gmail.draft_message", map[string]any{"subject": "S"}, json.RawMessage(`{"relay":true,"title":"t","body":"b"}`))
	if a == nil || a.Type != runtime.ArtifactNote {
		t.Fatalf("artifact = %+v, want a note", a)
	}
	d := turnArtifact("gmail.draft_message", map[string]any{"subject": "S"}, json.RawMessage(`{"id":"d1"}`))
	if d == nil || d.Type != runtime.ArtifactEmailDraft {
		t.Fatalf("artifact = %+v, want the draft unaffected", d)
	}
}

// TestNoteArtifactStreamsThroughTheDaemon is the end-to-end proof: an
// ordinary (non-draft) function's own connector result, flagged relay:
// true, streams as a note artifact event between that step's tool_start and
// tool_end, exactly where a draft artifact streams (TestDraftCallEmitsOneArtifact).
// gmail.list_messages stands in for the future linear.create_comment (Phase
// 7/U4 isn't built yet); the mechanism itself is fn-agnostic (turnArtifact's
// own doc comment), so this proves the wiring without needing that
// connector to exist.
func TestNoteArtifactStreamsThroughTheDaemon(t *testing.T) {
	h, g := newDraftHarness(t)
	g.output = json.RawMessage(`{"relay":true,"title":"CRA-3","body":"(simulated) Relayed: Nina, via Slack: it's fixed","source":"linear:CRA-3"}`)
	rec, _ := activeSink(h, "task_a")
	if out := h.invokeAsModel(t, gate.P0, gate.Clean, "gmail.list_messages", nil); out["status"] != "ok" {
		t.Fatalf("out = %+v, want ok", out)
	}
	events := rec.all()
	if len(events) != 3 || events[0].Kind != runtime.EventToolStart || events[1].Kind != runtime.EventArtifact || events[2].Kind != runtime.EventToolEnd {
		t.Fatalf("events = %+v, want tool_start, artifact, tool_end", events)
	}
	a := events[1].Artifact
	if a == nil || a.Type != runtime.ArtifactNote || a.Title != "CRA-3" || a.Source != "linear:CRA-3" || !a.Simulated {
		t.Fatalf("artifact = %+v", a)
	}
}
