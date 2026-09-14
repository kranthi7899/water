package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"water/internal/orchestrator"
)

func TestDashboardReadOnlyAndRenders(t *testing.T) {
	dir := t.TempDir()
	traces := filepath.Join(dir, "traces")
	cps := filepath.Join(dir, "checkpoints")
	os.MkdirAll(traces, 0o755)
	os.MkdirAll(cps, 0o755)
	trace := `{"type":"run_started","run_id":"r1","text":"should we migrate"}
{"type":"message","message":{"id":"msg-001","from":"user","to":"ceo","topic":"brief","payload":"should we migrate"}}
{"type":"backend_call","role":"ceo","backend":"fake-sub","metered":false,"duration_ms":12}
{"type":"node_finished","role":"ceo","duration_ms":15}
{"type":"tool_call","role":"cto","tool":{"role":"cto","tool":"read_file","args":{"path":"/x"},"allowed":false,"basis":"policy grants nothing","duration_ms":1}}
{"type":"run_finished","duration_ms":20}
`
	os.WriteFile(filepath.Join(traces, "r1.jsonl"), []byte(trace), 0o644)
	final := "migrate in phases"
	snap := orchestrator.Snapshot{RunID: "r1", Brief: "should we migrate", FinalOutput: &final, Visits: map[string]int{"ceo": 1}}
	b, _ := json.Marshal(snap)
	os.WriteFile(filepath.Join(cps, "r1.json"), b, 0o644)

	s, err := New(traces, cps, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()
	get := func(p string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, p, nil))
		return rr
	}
	if rr := get("/"); rr.Code != 200 || !strings.Contains(rr.Body.String(), "r1") {
		t.Fatalf("index: %d %s", rr.Code, rr.Body.String())
	}
	rr := get("/run/r1")
	if rr.Code != 200 {
		t.Fatalf("run: %d", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"migrate in phases", "read_file", "denied", "policy grants nothing", "fake-sub", "Diagnostics"} {
		if !strings.Contains(body, want) {
			t.Fatalf("run page missing %q", want)
		}
	}
	if rr := get("/api/run/r1"); rr.Code != 200 || !strings.Contains(rr.Body.String(), `"report"`) {
		t.Fatalf("api: %d", rr.Code)
	}
	// No write path.
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(m, "/run/r1", strings.NewReader("x")))
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s got %d, want 405", m, rr.Code)
		}
	}
	// Nothing on disk changed.
	entries, _ := os.ReadDir(traces)
	if len(entries) != 1 {
		t.Fatal("dashboard wrote into the trace dir")
	}
}
