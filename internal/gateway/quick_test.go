package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"water/internal/gate"
	"water/internal/store"
)

// invokeQuick calls /v1/quick/invoke with a freshly-minted turn token, the
// way the MCP bridge subprocess would on the model's behalf — mirroring
// invokeAsModel (toolinvoke_test.go) for the sous chef's own endpoint.
func (h *harness) invokeQuick(t *testing.T, origin gate.Origin, taint gate.Taint, function string, args map[string]any) map[string]any {
	t.Helper()
	tok := h.d.mintTurnToken(origin, taint, time.Hour)

	body, _ := json.Marshal(map[string]any{"function": function, "args": args})
	req, err := http.NewRequest(http.MethodPost, h.srv.URL+"/v1/quick/invoke", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestQuickInvokeOKReturnsStructuredOutput(t *testing.T) {
	h := newRoutingHarness(t)
	out := h.invokeQuick(t, gate.P0, gate.Clean, "quick.next_event", map[string]any{})
	if out["status"] != "ok" {
		t.Fatalf("out = %+v, want status=ok", out)
	}
}

func TestQuickInvokeUnknownIDIsDenied(t *testing.T) {
	h := newRoutingHarness(t)
	out := h.invokeQuick(t, gate.P0, gate.Clean, "quick.does_not_exist", map[string]any{})
	if out["status"] != "denied" {
		t.Fatalf("out = %+v, want status=denied", out)
	}
}

func TestQuickInvokeTaintedResultEscalatesSession(t *testing.T) {
	h := newRoutingHarness(t)
	if got := h.sessionTaint(t); got != gate.Clean {
		t.Fatalf("session taint before = %v, want clean", got)
	}
	if err := h.st.Upsert(context.Background(), &store.Event{
		Meta:    store.Meta{Source: "fake", SourceID: "ext1", External: true, CreatedAt: time.Now(), UpdatedAt: time.Now()},
		Title:   "External sync",
		StartAt: time.Now().Add(time.Hour),
		EndAt:   time.Now().Add(2 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	out := h.invokeQuick(t, gate.P0, gate.Clean, "quick.calendar", map[string]any{})
	if out["status"] != "ok" {
		t.Fatalf("out = %+v", out)
	}
	if got := h.sessionTaint(t); got != gate.Tainted {
		t.Fatalf("session taint after = %v, want tainted", got)
	}
}

func TestQuickFunctionSentToToolsInvokeIsDeniedWithoutGateAudit(t *testing.T) {
	h := newRoutingHarness(t)
	// A brand-new harness's audit log is empty (0 bytes): readAuditEntries
	// chokes on that (it json-decodes every line, and an empty file still
	// splits to one empty "line"), so this checks raw file size instead of
	// entry count, which stays correct however many entries exist.
	before, err := os.ReadFile(h.log.Path())
	if err != nil {
		t.Fatal(err)
	}

	out := h.invokeAsModel(t, gate.P0, gate.Clean, "quick.calendar", map[string]any{})
	if out["status"] != "denied" {
		t.Fatalf("out = %+v, want status=denied", out)
	}

	after, err := os.ReadFile(h.log.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("audit log size: before=%d after=%d, want unchanged (quick.calendar must never reach the gate)", len(before), len(after))
	}
}

// TestTwinToolPolicyDeclaresQuickTools proves the warm session's MCP bridge
// child actually sees quick.* tools declared alongside the twin's connector
// functions (TwinToolPolicy, daemon.go), and that the resulting policy is
// internally consistent (Validate).
func TestTwinToolPolicyDeclaresQuickTools(t *testing.T) {
	h := newRoutingHarness(t)
	pol := h.d.TwinToolPolicy()
	if len(pol.Quick) == 0 {
		t.Fatal("expected TwinToolPolicy to declare at least one quick tool")
	}
	found := false
	for _, qf := range pol.Quick {
		if qf.Tool == "quick__calendar" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Quick = %+v, missing quick__calendar", pol.Quick)
	}
	if err := pol.Validate(); err != nil {
		t.Fatalf("policy failed its own Validate: %v", err)
	}
}

func TestQuickInvokeParallel(t *testing.T) {
	h := newRoutingHarness(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out := h.invokeQuick(t, gate.P0, gate.Clean, "quick.next_event", map[string]any{})
			if out["status"] != "ok" {
				t.Errorf("parallel invoke: out = %+v, want status=ok", out)
			}
		}()
	}
	wg.Wait()
}
