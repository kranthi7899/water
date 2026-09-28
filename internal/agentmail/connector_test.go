package agentmail_test

import (
	"encoding/json"
	"testing"

	"water/internal/agentmail"
	"water/internal/connectors/google/gapi"
	"water/internal/twins"
)

// TestConnectorIsASecondIdentityOfGmail confirms the wrapper's whole job:
// its own registry name and its own (distinct) vault account, but it still
// offers gmail's real functions and schemas underneath (so the real,
// tested list_messages HTTP/incremental logic is what actually runs).
func TestConnectorIsASecondIdentityOfGmail(t *testing.T) {
	c := agentmail.New("water.twin@example.com")
	if c.Name() != agentmail.ConnectorName {
		t.Fatalf("Name() = %q, want %q", c.Name(), agentmail.ConnectorName)
	}
	svc, account := c.Credential()
	if svc != gapi.Service || account != agentmail.Account {
		t.Fatalf("Credential() = (%q, %q), want (%q, %q)", svc, account, gapi.Service, agentmail.Account)
	}
	if account == "ceo" {
		t.Fatal("agentmail must never resolve to the CEO's own gmail account")
	}
	var haveList bool
	for _, f := range c.Functions() {
		if f.Name == "list_messages" {
			haveList = true
			if f.Level != twins.R {
				t.Fatalf("list_messages level = %s, want R", f.Level)
			}
		}
	}
	if !haveList {
		t.Fatal("agentmail connector must still offer list_messages (inherited from gmail.Gmail)")
	}
}

// TestNormalizeNeverProducesStoreRecords is the isolation guarantee the
// package doc promises: whatever function name or payload, nothing this
// connector returns ever lands in the shared store (which would otherwise
// mix agent-mailbox content into the CEO's own mail signals).
func TestNormalizeNeverProducesStoreRecords(t *testing.T) {
	c := agentmail.New("water.twin@example.com")
	raw := json.RawMessage(`{"messages":[{"id":"m1","from":"x@y.com","subject":"s","body":"b"}],"history_id":"5"}`)
	recs, err := c.Normalize("list_messages", raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("Normalize returned %d record(s), want 0: %+v", len(recs), recs)
	}
}
