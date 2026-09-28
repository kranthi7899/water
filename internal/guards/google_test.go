package guards_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/connectors"
	"water/internal/connectors/google/gapi"
	"water/internal/connectors/google/gcal"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twins"
	"water/internal/vault"
)

const guardGoogleManifest = `
id: guard-google
name: Guard google twin
usage: {window: 1h, model_calls: 10}
connectors:
  - name: gcal
    functions:
      - {name: list_events, level: R}
`

// TestGuard_GoogleAccessTokenNeverLeaks runs a real gcal.list_events call
// through the gate against fake Google HTTP servers, then scans everything a
// CEO or an operator could read back — the tool output, the audit log, and
// every row and raw database file in the store — for the access token
// gapi's client used. gapi.Client already scrubs it from errors (see
// gapi/client_test.go); this is the end-to-end guard that the whole gate
// path never re-introduces it anywhere else.
func TestGuard_GoogleAccessTokenNeverLeaks(t *testing.T) {
	const accessToken = "ya29.a0Afake-GUARD-TEST-ACCESS-TOKEN-should-never-appear"
	dir := t.TempDir()

	tok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": accessToken, "expires_in": 3600, "token_type": "Bearer"})
	}))
	defer tok.Close()

	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+accessToken {
			t.Errorf("request reached the fake API without the expected bearer token: %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"items":[{"id":"e1","summary":"Board sync","htmlLink":"https://cal.example/e1",`+
			`"start":{"dateTime":"2026-09-23T10:00:00Z"},"end":{"dateTime":"2026-09-23T11:00:00Z"}}]}`)
	}))
	defer api.Close()

	noSleep := func(context.Context, time.Duration) error { return nil }
	reg, err := connectors.NewRegistry(gcal.NewWithOptions(&gapi.Options{BaseURL: api.URL, TokenURL: tok.URL, Sleep: noSleep}))
	if err != nil {
		t.Fatal(err)
	}
	m, err := twins.Parse([]byte(guardGoogleManifest))
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "water.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log, err := audit.Open(filepath.Join(dir, "audit", "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	q := approvals.NewQueue(st, log)
	v := vault.NewMemory()
	cred := gapi.Credential{ClientID: "guard-client-id", ClientSecret: "GOCSPX-guard-secret", RefreshToken: "1//guard-refresh-token"}
	secret, err := cred.Secret()
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Set(gapi.Service, gapi.DefaultAccount, secret); err != nil {
		t.Fatal(err)
	}
	g, err := gate.New(gate.Config{Manifest: m, Registry: reg, Approvals: q, Audit: log, Vault: v, Store: st})
	if err != nil {
		t.Fatal(err)
	}

	res, err := g.Invoke(context.Background(), gate.Call{
		Function: "gcal.list_events",
		Args:     map[string]any{"time_min": "2026-09-23T00:00:00Z", "time_max": "2026-09-30T00:00:00Z"},
		Origin:   gate.P0, Taint: gate.Clean,
	})
	if err != nil {
		t.Fatal(err)
	}

	var seen []string
	seen = append(seen, string(res.Output), fmt.Sprintf("%+v", res))
	b, err := os.ReadFile(log.Path())
	if err != nil {
		t.Fatal(err)
	}
	seen = append(seen, string(b))
	seen = append(seen, dumpDB(t, dbPath)...)
	for _, s := range seen {
		if strings.Contains(s, accessToken) {
			t.Fatalf("access token leaked into: %.300s", s)
		}
	}
}
