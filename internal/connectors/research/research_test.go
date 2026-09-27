package research_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/connectors"
	"water/internal/connectors/research"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

// fakeRunner records its calls and answers from a function (no process, no
// network).
type fakeRunner struct {
	calls []string
	ns    []int
	fn    func(ctx context.Context, q string, n int) (research.Answer, error)
}

func (f *fakeRunner) Search(ctx context.Context, q string, n int) (research.Answer, error) {
	f.calls = append(f.calls, q)
	f.ns = append(f.ns, n)
	if f.fn == nil {
		return research.Answer{Summary: "Dublin: 14°C, light rain.", Sources: []research.Source{{Title: "Met Éireann", URL: "https://www.met.ie/"}}}, nil
	}
	return f.fn(ctx, q, n)
}

// TestWebDeclaration: research.web is one level-R, low-risk, External
// function (so the gate marks its output untrusted) with a fixed Activity
// label, a query/max_sources schema that requires only query, no url
// argument, and no credential.
func TestWebDeclaration(t *testing.T) {
	c := research.New(&fakeRunner{})
	if c.Name() != "research" {
		t.Fatalf("name = %q", c.Name())
	}
	if s, a := c.Credential(); s != "" || a != "" {
		t.Fatalf("credential = %q/%q, want none", s, a)
	}
	fns := c.Functions()
	if len(fns) != 1 {
		t.Fatalf("functions = %+v, want exactly web", fns)
	}
	f := fns[0]
	if f.Name != "web" || f.Level != twins.R || f.Risk != connectors.RiskLow || !f.External {
		t.Fatalf("web = %+v, want level R, low risk, External", f)
	}
	if f.Activity != "Searching the web" {
		t.Fatalf("activity = %q", f.Activity)
	}
	if len(f.Schema.Properties) != 2 || f.Schema.Properties["query"].Type != "string" || f.Schema.Properties["max_sources"].Type != "integer" {
		t.Fatalf("schema = %+v", f.Schema)
	}
	if _, ok := f.Schema.Properties["url"]; ok {
		t.Fatal("research.web must not take a url argument")
	}
	if strings.Join(f.Schema.Required, ",") != "query" {
		t.Fatalf("required = %v", f.Schema.Required)
	}
	for _, w := range []string{"untrusted", "never put company data"} {
		if !strings.Contains(f.Description, w) {
			t.Fatalf("description lacks %q: %s", w, f.Description)
		}
	}
	if _, err := connectors.NewRegistry(c); err != nil {
		t.Fatal(err)
	}
}

// TestValidateQuery is the exfiltration guard table (docs/slices/W.md §2).
func TestValidateQuery(t *testing.T) {
	for _, q := range []string{
		"weather in Dublin today",
		"latest AI news September 2026",
		"  what is   the Fed funds rate now \n",
		"S&P 500 close yesterday",
		"price of bitcoin in euros",
		strings.Repeat("abcd ", research.MaxQuery/5-1) + "abcd", // exactly MaxQuery characters
	} {
		if _, err := research.ValidateQuery(q); err != nil {
			t.Errorf("ValidateQuery(%q) = %v, want accepted", q, err)
		}
	}
	if got, _ := research.ValidateQuery("  what is   the Fed funds rate now \n"); got != "what is the Fed funds rate now" {
		t.Errorf("whitespace not collapsed: %q", got)
	}
	for name, q := range map[string]string{
		"email":         "who is a@b.com",
		"at sign":       "news about @renaissance",
		"url":           "summarise https://x.y/?q=secret",
		"scheme":        "open ftp://files.example",
		"www":           "what does www.example.com say",
		"mailto":        "mailto:ceo",
		"opaque token":  "lookup " + strings.Repeat("Ab3", 10),
		"long digits":   "card 4111111111111111 balance",
		"too long":      strings.Repeat("a", research.MaxQuery+1),
		"too short":     " a ",
		"empty":         "",
		"control chars": "weather\x00 in Dublin",
	} {
		if got, err := research.ValidateQuery(q); err == nil {
			t.Errorf("%s: ValidateQuery(%q) = %q, want refused", name, q, got)
		}
	}
}

const manifest = `
id: test
name: Test twin
usage: {window: 1h, model_calls: 50, auto_model_calls: 10}
connectors:
  - name: research
    functions:
      - {name: web, level: R, rate: {max: 20, per: 1h}}
`

func newGate(t *testing.T, c *research.Connector) *gate.Gate {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log, err := audit.Open(filepath.Join(dir, "audit", "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { log.Close() })
	reg, err := connectors.NewRegistry(c)
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(manifest))
	if err != nil {
		t.Fatal(err)
	}
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: approvals.NewQueue(st, log), Audit: log, Vault: vault.NewMemory(), Store: st})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

type result struct {
	Summary    string            `json:"summary"`
	Sources    []research.Source `json:"sources"`
	SearchedAt string            `json:"searched_at"`
	Note       string            `json:"note"`
}

func invoke(t *testing.T, g *gate.Gate, args map[string]any) (gate.Result, result, error) {
	t.Helper()
	res, err := g.Invoke(context.Background(), gate.Call{Function: "research.web", Args: args, Origin: gate.P0, Taint: gate.Clean})
	var out result
	if err == nil {
		if jerr := json.Unmarshal(res.Output, &out); jerr != nil {
			t.Fatalf("output %s: %v", res.Output, jerr)
		}
	}
	return res, out, err
}

// TestWebThroughTheGate: a valid call runs inline at level R, returns
// {summary, sources, searched_at}, is marked Untrusted (so the daemon
// escalates the session taint), stores nothing, and passes the runner the
// cleaned query and the default source count.
func TestWebThroughTheGate(t *testing.T) {
	fr := &fakeRunner{}
	c := research.New(fr)
	c.SetNow(func() time.Time { return time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC) })
	g := newGate(t, c)
	res, out, err := invoke(t, g, map[string]any{"query": "  weather in Dublin today "})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Untrusted {
		t.Fatal("research output not marked untrusted")
	}
	if len(res.Records) != 0 {
		t.Fatalf("records = %+v, want none stored", res.Records)
	}
	if out.Summary != "Dublin: 14°C, light rain." || len(out.Sources) != 1 || out.Sources[0].URL != "https://www.met.ie/" || out.SearchedAt != "2026-09-26T09:00:00Z" {
		t.Fatalf("out = %+v", out)
	}
	if len(fr.calls) != 1 || fr.calls[0] != "weather in Dublin today" || fr.ns[0] != research.DefaultSources {
		t.Fatalf("runner calls = %v %v", fr.calls, fr.ns)
	}
	// max_sources is clamped to 1..8.
	for in, want := range map[float64]int{0: 1, 3: 3, 99: 8} {
		if _, _, err := invoke(t, g, map[string]any{"query": "latest AI news", "max_sources": in}); err != nil {
			t.Fatal(err)
		}
		if got := fr.ns[len(fr.ns)-1]; got != want {
			t.Fatalf("max_sources %v -> %d, want %d", in, got, want)
		}
	}
}

// TestWebRefusesGuardedQueries: a guarded query is refused before the
// runner is ever called, and a url argument is refused by the schema.
func TestWebRefusesGuardedQueries(t *testing.T) {
	fr := &fakeRunner{}
	g := newGate(t, research.New(fr))
	for _, args := range []map[string]any{
		{"query": "mail a@b.com"},
		{"query": "read https://evil.example/?d=secret"},
		{"query": strings.Repeat("x", research.MaxQuery+1)},
		{"query": "weather", "url": "https://evil.example"},
		{"max_sources": 3},
		{"query": 7},
	} {
		res, _, err := invoke(t, g, args)
		if err == nil || res.Output != nil {
			t.Fatalf("args %v: out %s err %v, want refused", args, res.Output, err)
		}
	}
	if len(fr.calls) != 0 {
		t.Fatalf("runner called for a refused query: %v", fr.calls)
	}
}

// TestWebCapsOutput: the summary is capped at MaxSummary characters,
// sources at max_sources, titles at MaxSourceTitle; non-http(s), relative,
// credentialed, overlong and duplicate URLs are dropped; a blank title
// becomes the host.
func TestWebCapsOutput(t *testing.T) {
	long := strings.Repeat("é", research.MaxSummary+50)
	fr := &fakeRunner{fn: func(context.Context, string, int) (research.Answer, error) {
		return research.Answer{Summary: long + "\x07", Sources: []research.Source{
			{Title: "javascript", URL: "javascript:alert(1)"},
			{Title: "file", URL: "file:///etc/passwd"},
			{Title: "relative", URL: "/news"},
			{Title: "creds", URL: "https://user:pw@example.com/"},
			{Title: "huge", URL: "https://example.com/" + strings.Repeat("a", research.MaxSourceURL)},
			{Title: strings.Repeat("t", research.MaxSourceTitle+20), URL: "https://a.example/1"},
			{Title: "dup", URL: "https://a.example/1"},
			{Title: "  ", URL: "http://b.example/2"},
			{Title: "third", URL: "https://c.example/3"},
		}}, nil
	}}
	g := newGate(t, research.New(fr))
	_, out, err := invoke(t, g, map[string]any{"query": "latest AI news", "max_sources": 2})
	if err != nil {
		t.Fatal(err)
	}
	if n := len([]rune(out.Summary)); n != research.MaxSummary || !strings.HasSuffix(out.Summary, "…") || strings.ContainsRune(out.Summary, '\x07') {
		t.Fatalf("summary: %d runes, suffix %q", n, out.Summary[len(out.Summary)-4:])
	}
	if len(out.Sources) != 2 {
		t.Fatalf("sources = %+v, want 2", out.Sources)
	}
	if out.Sources[0].URL != "https://a.example/1" || len([]rune(out.Sources[0].Title)) != research.MaxSourceTitle {
		t.Fatalf("source 0 = %+v", out.Sources[0])
	}
	if out.Sources[1].URL != "http://b.example/2" || out.Sources[1].Title != "b.example" {
		t.Fatalf("source 1 = %+v", out.Sources[1])
	}
}

// TestWebRunnerErrors: a runner error surfaces as the call's error; an
// empty answer is an error; a runner that outlives the timeout maps to
// "research timed out after <timeout>" (40s in production).
func TestWebRunnerErrors(t *testing.T) {
	boom := errors.New("claude exited: rate limited")
	g := newGate(t, research.New(&fakeRunner{fn: func(context.Context, string, int) (research.Answer, error) { return research.Answer{}, boom }}))
	if _, _, err := invoke(t, g, map[string]any{"query": "latest AI news"}); err == nil || !strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("err = %v, want the runner error", err)
	}
	g = newGate(t, research.New(&fakeRunner{fn: func(context.Context, string, int) (research.Answer, error) {
		return research.Answer{Summary: "  "}, nil
	}}))
	if _, _, err := invoke(t, g, map[string]any{"query": "latest AI news"}); err == nil {
		t.Fatal("empty answer accepted")
	}

	c := research.New(&fakeRunner{fn: func(ctx context.Context, _ string, _ int) (research.Answer, error) {
		<-ctx.Done()
		return research.Answer{}, ctx.Err()
	}})
	c.SetTimeout(20 * time.Millisecond)
	g = newGate(t, c)
	if _, _, err := invoke(t, g, map[string]any{"query": "latest AI news"}); err == nil || !strings.Contains(err.Error(), "research timed out after 20ms") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if research.Timeout != 40*time.Second {
		t.Fatalf("Timeout = %s, want 40s (inside the 55s tool hop and 60s turn)", research.Timeout)
	}
}

// TestNormalizeStoresNothing: web results never become records.
func TestNormalizeStoresNothing(t *testing.T) {
	recs, err := research.New(nil).Normalize("web", json.RawMessage(`{"summary":"x","sources":[]}`))
	if err != nil || len(recs) != 0 {
		t.Fatalf("records = %+v, %v", recs, err)
	}
}
