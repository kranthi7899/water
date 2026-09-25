package tools

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// fakeQuickDaemon serves both POST /v1/tools/invoke and POST
// /v1/quick/invoke on the same socket, recording which path each request
// actually hit, so dispatch can be proven directly rather than inferred
// from a single endpoint's behavior.
func fakeQuickDaemon(t *testing.T) (socketPath string, hits *[]string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "water-quick-sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socketPath = filepath.Join(dir, "s.sock")
	l, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	var seen []string
	hits = &seen
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/tools/invoke", func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, "twin")
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "output": json.RawMessage(`"twin-result"`)})
	})
	mux.HandleFunc("/v1/quick/invoke", func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, "quick")
		json.NewEncoder(w).Encode(map[string]any{"status": "ok", "output": json.RawMessage(`"quick-result"`)})
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(l)
	t.Cleanup(func() {
		srv.Close()
		os.Remove(socketPath)
	})
	return socketPath, hits
}

func quickAndTwinPolicy(socket, token string) *Policy {
	twinSchema, _ := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{}})
	quickSchema, _ := json.Marshal(map[string]any{"type": "object", "properties": map[string]any{}})
	return &Policy{
		Role:       "ceo",
		Twin:       []TwinFunction{{ID: "gcal.list_events", Tool: "gcal__list_events", Description: "List events.", Schema: twinSchema}},
		Quick:      []QuickFunction{{ID: "quick.calendar", Tool: "quick__calendar", Description: "List events (quick).", Schema: quickSchema}},
		TwinSocket: socket,
		TwinToken:  token,
	}
}

func TestPolicyValidateRejectsCollisionsAndBadNames(t *testing.T) {
	schema, _ := json.Marshal(map[string]any{"type": "object"})
	cases := []struct {
		name string
		pol  Policy
	}{
		{"twin named like a quick tool", Policy{Twin: []TwinFunction{{ID: "x.y", Tool: "quick__y", Schema: schema}}}},
		{"quick without quick. prefix", Policy{Quick: []QuickFunction{{ID: "store.calendar_events", Tool: "quick__calendar", Schema: schema}}}},
		{"twin/quick tool-name collision", Policy{
			Twin:  []TwinFunction{{ID: "gcal.list_events", Tool: "quick__calendar", Schema: schema}},
			Quick: []QuickFunction{{ID: "quick.calendar", Tool: "quick__calendar", Schema: schema}},
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := c.pol.Validate(); err == nil {
				t.Fatalf("expected Validate to reject: %s", c.name)
			}
		})
	}
}

func TestPolicyValidateAcceptsWellFormed(t *testing.T) {
	pol := quickAndTwinPolicy("sock", "tok")
	if err := pol.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestPolicyEmptyConsidersQuick(t *testing.T) {
	schema, _ := json.Marshal(map[string]any{"type": "object"})
	pol := &Policy{Quick: []QuickFunction{{ID: "quick.calendar", Tool: "quick__calendar", Schema: schema}}}
	if pol.Empty() {
		t.Error("a policy with only quick tools must not report Empty()")
	}
}

func TestDefinitionsIncludesBothKinds(t *testing.T) {
	pol := quickAndTwinPolicy("sock", "tok")
	svc := NewService(pol, nil)
	defs := svc.Definitions()
	names := map[string]bool{}
	for _, d := range defs {
		names[d.Name] = true
	}
	if !names["gcal__list_events"] || !names["quick__calendar"] {
		t.Fatalf("Definitions() = %+v, missing one kind", defs)
	}
}

func TestCallDispatchesQuickAndTwinToDistinctEndpoints(t *testing.T) {
	socket, hits := fakeQuickDaemon(t)
	pol := quickAndTwinPolicy(socket, "tok")
	svc := NewService(pol, nil)

	if _, err := svc.Call(context.Background(), "gcal__list_events", map[string]any{}); err != nil {
		t.Fatalf("twin call: %v", err)
	}
	if _, err := svc.Call(context.Background(), "quick__calendar", map[string]any{}); err != nil {
		t.Fatalf("quick call: %v", err)
	}
	if len(*hits) != 2 || (*hits)[0] != "twin" || (*hits)[1] != "quick" {
		t.Fatalf("hits = %v, want [twin quick]", *hits)
	}
}
