package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/gate"
	"water/internal/store"
	"water/internal/twinlink"
)

// twinSendFunction is the one connector function that sends a twin message.
const twinSendFunction = "twinlink.send_message"

// peerAuth admits only another twin's token — a clients.json entry named
// "twin:<peer id>" — and hands the handler that peer id, the only from_twin
// the request may claim. A client token (the CLI, the macOS app) is refused
// here, and auth refuses a peer token everywhere else: a peer can deliver a
// message, never run a turn, read state, or answer an approval.
func (d *Daemon) peerAuth(h func(http.ResponseWriter, *http.Request, string)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := bearerToken(r)
		if !ok {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		name, ok := d.cfg.Clients.Valid(tok)
		peer, isPeer := strings.CutPrefix(name, twinlink.PeerClientPrefix)
		if !ok || !isPeer || !twinlink.ValidTwinID(peer) {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		h(w, r, peer)
	})
}

// isPeerClient reports whether a client name belongs to another twin.
func isPeerClient(name string) bool { return strings.HasPrefix(name, twinlink.PeerClientPrefix) }

// handleTwinReceive records one message from another twin. The message is
// untrusted external content, unconditionally: the session taint escalates
// first, before the body is even read (the same posture as a meeting
// segment), and the only thing that happens to the message is that it is
// validated and stored. It is never run, never proposed as an action, and
// never reaches a model here; the CEO's own twin can read it later, as
// untrusted data, through twininbox.list_messages.
func (d *Daemon) handleTwinReceive(w http.ResponseWriter, r *http.Request, peer string) {
	d.escalateTaint(true)
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, twinlink.MaxWireBody))
	dec.DisallowUnknownFields()
	var m twinlink.Message
	if err := dec.Decode(&m); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := m.Validate(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if m.FromTwin != peer {
		http.Error(w, "from_twin does not match the sending twin's token", http.StatusForbidden)
		return
	}
	if m.ToTwin != d.cfg.Manifest.ID {
		// No forwarding and no multi-hop: a message for another twin is
		// refused, never relayed.
		http.Error(w, "to_twin is not this twin", http.StatusBadRequest)
		return
	}
	if id, err := twinlink.DeriveID(m); err != nil || id != m.ID {
		http.Error(w, "id does not match the message content", http.StatusBadRequest)
		return
	}
	if m.Type == twinlink.Response {
		// A response must answer a request this twin actually sent to that
		// same peer; the unique index then allows only one.
		req, err := d.cfg.Store.GetTwinMessage(r.Context(), store.TwinOutbound, m.InReplyTo)
		if err != nil || req.ToTwin != peer || req.Type != string(twinlink.Request) {
			http.Error(w, "in_reply_to is not a request this twin sent to you", http.StatusConflict)
			return
		}
	}
	row, err := twinlink.Row(store.TwinInbound, m, time.Now())
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// On the record before it is stored, like every other effect: an
	// inbound message that cannot be audited is not accepted.
	if _, err := d.cfg.Audit.Append(audit.Record{Kind: audit.KindReceive, Function: "twinlink.receive", Origin: "twin:" + peer,
		Allowed: true, Reason: "recorded as untrusted data: " + string(m.Type) + " " + m.ID, ArgsHash: row.ContentHash}); err != nil {
		http.Error(w, "audit unavailable", http.StatusServiceUnavailable)
		return
	}
	inserted, err := d.cfg.Store.InsertTwinMessage(r.Context(), row)
	switch {
	case errors.Is(err, store.ErrTwinMessageConflict), errors.Is(err, store.ErrTwinResponseExists):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		http.Error(w, "could not record the message", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, twinlink.Ack{Accepted: true, ID: m.ID, Duplicate: !inserted})
}

// handleTwinList lists stored twin messages for a client (`water twin
// inbox`). Like the meeting cues endpoint, it only reads already-recorded,
// already-tainted content for display and reaches no model, so it does not
// escalate taint itself.
func (d *Daemon) handleTwinList(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	l, err := twinlink.List(r.Context(), d.cfg.Store, r.URL.Query().Get("direction"), limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

// TwinOutboxResult is what POST /v1/twinlink/outbox returns: the pending
// approval for the message and the exact read-back the CEO decides on.
type TwinOutboxResult struct {
	ApprovalID  string `json:"approval_id"`
	PayloadHash string `json:"payload_hash"`
	MessageID   string `json:"message_id"`
	ReadBack    string `json:"read_back"`
}

// handleTwinOutbox stages a twin message the CEO asked for (`water twin
// send` / `water twin reply`) as a pending twinlink.send_message approval,
// through the existing queue, exactly the way the model-tool bridge stages
// any other level-A call. Nothing is sent here: the message leaves only
// when the CEO approves this envelope through the normal approval flow.
func (d *Daemon) handleTwinOutbox(w http.ResponseWriter, r *http.Request) {
	var args map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, twinlink.MaxWireBody)).Decode(&args); err != nil || args == nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if _, ok := d.cfg.Manifest.Function(twinSendFunction); !ok {
		http.Error(w, twinSendFunction+" is not in the "+d.cfg.Manifest.ID+" manifest", http.StatusForbidden)
		return
	}
	_, spec, ok := d.cfg.Registry.Lookup(twinSendFunction)
	if !ok {
		http.Error(w, "no connector provides "+twinSendFunction, http.StatusForbidden)
		return
	}
	if err := spec.Schema.Validate(args); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	m, err := twinlink.MessageFromArgs(d.cfg.Manifest.ID, args, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	env, err := d.cfg.Approvals.Propose(r.Context(), approvals.Envelope{
		Action: twinSendFunction, Recipient: "twin:" + m.ToTwin, Payload: args, EvidenceRefs: m.EvidenceRefs,
		Risk: string(spec.Risk), Origin: string(gate.P0),
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	d.notifyApprovalRequired(env)
	writeJSON(w, http.StatusOK, TwinOutboxResult{ApprovalID: env.ID, PayloadHash: env.PayloadHash, MessageID: m.ID, ReadBack: approvals.ReadBack(env)})
}
