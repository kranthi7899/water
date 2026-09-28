// Package agentmail is the daemon's inbound-triage watcher: it polls a
// second Gmail account -- the agent's own verified mailbox (the same
// address config's agent.mail_address names as the "Send mail as" alias) --
// for mail addressed to the agent itself versus mail actually meant for the
// CEO, and turns each new message into either a quiet morning-brief signal
// or a staged forward the CEO must approve like any other level-A action.
//
// Design note: this is deliberately session-less, much simpler than Slice
// M's live-meeting sessions -- there is no lifecycle to start or stop, only
// an incremental poll on its own tick (Watcher.Tick), wired as a third loop
// in internal/sync.Refresher.Run next to the existing mail/events ticks.
package agentmail

import (
	"encoding/json"

	"water/internal/connectors"
	"water/internal/connectors/google/gapi"
	"water/internal/connectors/google/gmail"
	"water/internal/store"
)

// Account is the vault account name the agent's own mailbox credential is
// stored under: `water connect google --account agent` (docs/agent-
// identity-setup.md). It is distinct from gapi.DefaultAccount, the CEO's
// own real mailbox's account name.
const Account = "agent"

// ConnectorName is this connector's registry/manifest name:
// "agentmail.list_messages", never "gmail.list_messages" -- so it can never
// be confused with, or accidentally inherit a manifest grant meant for, the
// CEO's own gmail connector. twins/ceo/twin.yaml grants only
// "agentmail.list_messages"; nothing else this type happens to implement
// (get_message, draft_message, send_message) is ever reachable through it,
// since the gate checks the manifest before it ever looks at a connector.
const ConnectorName = "agentmail"

// mailbox is *gmail.Gmail wearing a second identity: the same real Gmail
// HTTP calls (including its tested incremental history-based walk for
// list_messages) run against the agent's own vault credential instead of
// the CEO's. Only Name, Credential and Normalize are overridden.
type mailbox struct {
	*gmail.Gmail
}

// New builds the connector twin.yaml's "agentmail" entry resolves to.
// mailAddress is the same agent.mail_address config value gmail.New reads;
// it is never used by list_messages/get_message (only draft/send set
// From), but passing it keeps this honestly "the agent's own mailbox," not
// some third, differently-identified account.
func New(mailAddress string) connectors.Connector {
	return &mailbox{Gmail: gmail.New(mailAddress)}
}

func (*mailbox) Name() string                 { return ConnectorName }
func (*mailbox) Credential() (string, string) { return gapi.Service, Account }

// Normalize deliberately discards everything. An agent-mailbox message must
// never land in the shared store.Message table: nothing that reads it
// (internal/runtime/brief.go's "needs attention" heuristic, internal/
// decisions.Trigger's candidate scan, sync's own mail-prefetch queries)
// filters by Source, so it would otherwise silently mix into the CEO's own
// mail signals and decision candidates. Watcher reads each tick's messages
// directly from the call's raw JSON output instead (see watcher.go), so
// nothing here is lost -- it is simply never treated as the CEO's own mail.
func (*mailbox) Normalize(string, json.RawMessage) ([]store.Record, error) { return nil, nil }
