// Package gateway is the water daemon: a single long-running process that
// owns the gate, the approval queue, the audit log, the store and the model
// sessions, serving every client (water chat, water ask, the macOS app) over
// one local HTTP-over-Unix-socket API.
package gateway

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/backend"
	"water/internal/connectors"
	"water/internal/decisions"
	"water/internal/gate"
	"water/internal/meetings"
	"water/internal/runtime"
	"water/internal/store"
	"water/internal/tools"
	"water/internal/twins"
)

// Config wires the daemon to one twin's runtime dependencies. All fields are
// required except Warm.
type Config struct {
	Manifest  *twins.Manifest
	Store     *store.Store
	Audit     *audit.Log
	Approvals *approvals.Queue
	Gate      *gate.Gate
	Registry  *connectors.Registry
	Backend   backend.Backend
	Warm      *backend.WarmSession // optional; preferred for fast-tier turns
	RoleMD    string
	// Decisions runs the classification-trigger orchestration (see
	// internal/decisions.Trigger) over today's candidate items. Optional: a
	// nil Decisions makes /v1/decisions report no cards and the morning
	// brief's open-cards signal stay absent, rather than erroring.
	Decisions *decisions.Trigger
	// ProactiveCues gates GET /v1/meetings/{id}/cues (docs/slices/M.md
	// section 6, config key meetings.proactive_cues). Off by default: the
	// endpoint still exists and always answers 200, just with an empty,
	// enabled:false body, so a client can probe for the feature safely.
	ProactiveCues bool
	Clients       *Clients
	// SocketPath is this daemon's own socket, handed to the twin-mode MCP
	// bridge so a model-initiated tool call can reach back in.
	SocketPath string
}

// turnAuth is what a per-turn tool-proxy token grants: the origin and taint
// of the turn that minted it, so a model-initiated call is authorized exactly
// as if the gate were checking the live turn.
type turnAuth struct {
	Origin  gate.Origin
	Taint   gate.Taint
	Expires time.Time
}

// Daemon serves the HTTP API described in docs/slices/A2.md.
type Daemon struct {
	cfg      Config
	meetings *meetings.Manager

	mu           sync.Mutex
	tasks        map[string]context.CancelFunc
	turnTok      map[string]turnAuth
	sessionToken string // the one long-lived tool-proxy token; see stableSessionToken
	// sinks are the open turn streams (by task id) that a queued tool call
	// is reported to as approval_required; see notifyApprovalRequired.
	sinks map[string]*turnSink
}

func New(cfg Config) *Daemon {
	return &Daemon{cfg: cfg, meetings: meetings.New(cfg.Store), tasks: map[string]context.CancelFunc{}, turnTok: map[string]turnAuth{}, sinks: map[string]*turnSink{}}
}

// turnSink is one open POST /v1/turns stream. Writes from the turn itself
// and from a concurrent tool-invoke handler are serialized, and nothing is
// written once the stream's final done/error event has gone out.
type turnSink struct {
	mu     sync.Mutex
	closed bool
	write  func(runtime.Event)
}

func (s *turnSink) emit(e runtime.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.write(e)
	if e.Kind == runtime.EventDone || e.Kind == runtime.EventError {
		s.closed = true
	}
}

func (s *turnSink) close() {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

func (d *Daemon) registerSink(id string, s *turnSink) {
	d.mu.Lock()
	d.sinks[id] = s
	d.mu.Unlock()
}

func (d *Daemon) unregisterSink(id string) {
	d.mu.Lock()
	delete(d.sinks, id)
	d.mu.Unlock()
}

// notifyApprovalRequired reports a tool call the model made that was queued
// for approval to every open turn stream. Every turn shares the one session
// tool token (see stableSessionToken), so a call cannot be tied to a single
// turn by its token; in practice the warm session serializes turns, so there
// is one open stream, and the daemon serves one principal either way. A call
// that lands after its turn's stream closed is still queued, just not
// announced inline (the brief and `water approve` still list it).
func (d *Daemon) notifyApprovalRequired(env approvals.Envelope) {
	d.mu.Lock()
	sinks := make([]*turnSink, 0, len(d.sinks))
	for _, s := range d.sinks {
		sinks = append(sinks, s)
	}
	d.mu.Unlock()
	for _, s := range sinks {
		s.emit(runtime.Event{Kind: runtime.EventApprovalRequired, ApprovalID: env.ID, Text: env.Action})
	}
}

func newID(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}

// Mux builds the HTTP handler, with token auth applied to every route.
func (d *Daemon) Mux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/health", d.handleHealth) // unauthenticated: a liveness probe only
	mux.Handle("POST /v1/turns", d.auth(d.handleTurn))
	mux.Handle("GET /v1/approvals", d.auth(d.handleListApprovals))
	mux.Handle("POST /v1/approvals/{id}/decision", d.auth(d.handleDecideApproval))
	mux.Handle("GET /v1/state", d.auth(d.handleState))
	mux.Handle("GET /v1/decisions", d.auth(d.handleListDecisions))
	mux.Handle("POST /v1/tasks/{id}/cancel", d.auth(d.handleCancel))
	mux.Handle("POST /v1/meetings/start", d.auth(d.handleMeetingStart))
	mux.Handle("POST /v1/meetings/{id}/segments", d.auth(d.handleMeetingSegment))
	mux.Handle("POST /v1/meetings/{id}/stop", d.auth(d.handleMeetingStop))
	mux.Handle("GET /v1/meetings/{id}/cues", d.auth(d.handleMeetingCues))
	// /v1/tools/invoke is authenticated separately (a per-turn token, not a
	// client token): it is called by the MCP bridge subprocess, not a client.
	mux.HandleFunc("POST /v1/tools/invoke", d.handleToolInvoke)
	return mux
}

func (d *Daemon) auth(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tok, ok := bearerToken(r)
		if !ok {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		if _, ok := d.cfg.Clients.Valid(tok); !ok {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		h(w, r)
	})
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	const p = "Bearer "
	if len(h) <= len(p) || h[:len(p)] != p {
		return "", false
	}
	return h[len(p):], true
}

func (d *Daemon) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "twin": d.cfg.Manifest.ID})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// registerTask associates a task id with a cancel func for the duration of
// one turn.
func (d *Daemon) registerTask(id string, cancel context.CancelFunc) {
	d.mu.Lock()
	d.tasks[id] = cancel
	d.mu.Unlock()
}

func (d *Daemon) unregisterTask(id string) {
	d.mu.Lock()
	delete(d.tasks, id)
	d.mu.Unlock()
}

func (d *Daemon) handleCancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	d.mu.Lock()
	cancel, ok := d.tasks[id]
	d.mu.Unlock()
	if !ok {
		http.Error(w, "no such task", http.StatusNotFound)
		return
	}
	cancel()
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": id})
}

// maxMeetingBody bounds a meetings request body: a segment is a sentence or
// two of text, never audio.
const maxMeetingBody = 64 << 10

func (d *Daemon) handleMeetingStart(w http.ResponseWriter, r *http.Request) {
	var body struct {
		EventID string `json:"event_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMeetingBody)).Decode(&body); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	s, err := d.meetings.Start(r.Context(), body.EventID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": s.ID, "started_at": s.StartedAt})
}

// handleMeetingSegment records one transcript segment. Meeting speech is
// untrusted on both channels, the CEO's own mic included (a hot mic hears
// anyone near it): the session is escalated to tainted first, before the
// body is even read, so no failure path can skip it. Only an explicit
// /v1/turns request is ever an instruction to the twin.
func (d *Daemon) handleMeetingSegment(w http.ResponseWriter, r *http.Request) {
	d.escalateTaint(true)
	var body struct {
		At      time.Time `json:"at"`
		Channel string    `json:"channel"`
		Text    string    `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMeetingBody)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	err := d.meetings.AddSegment(r.Context(), r.PathValue("id"), meetings.Segment{At: body.At, Channel: meetings.Channel(body.Channel), Text: body.Text})
	if err != nil {
		meetingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (d *Daemon) handleMeetingStop(w http.ResponseWriter, r *http.Request) {
	s, err := d.meetings.Stop(r.Context(), r.PathValue("id"))
	if err != nil {
		meetingError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": s.ID, "ended_at": s.EndedAt})
}

// handleMeetingCues serves docs/slices/M.md section 6's quiet proactive
// cues, behind the meetings.proactive_cues config flag (default false).
// This is a read of already-tainted meeting content for the client's own
// side panel, not a new instruction and not a model call, so unlike
// handleMeetingSegment it does not escalate taint. Disabled answers 200
// with enabled:false and no items, so a client can probe for the feature
// safely; an unknown or malformed id 404s exactly like the other meeting
// endpoints.
func (d *Daemon) handleMeetingCues(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !d.cfg.ProactiveCues {
		writeJSON(w, http.StatusOK, map[string]any{"session_id": id, "enabled": false, "items": []meetings.CueItem{}})
		return
	}
	cs, err := d.meetings.Cues(r.Context(), id, time.Now())
	if err != nil {
		meetingError(w, err)
		return
	}
	items := cs.Items
	if items == nil {
		items = []meetings.CueItem{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"session_id": cs.SessionID, "enabled": true, "items": items})
}

func meetingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, meetings.ErrNotFound):
		http.Error(w, "no such meeting session", http.StatusNotFound)
	case errors.Is(err, meetings.ErrEnded):
		http.Error(w, err.Error(), http.StatusConflict)
	case errors.Is(err, meetings.ErrBadSegment):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// mintTurnToken issues a short-lived token scoped to one turn's origin and
// taint, for the MCP bridge to present at /v1/tools/invoke. Expired tokens
// are swept lazily on lookup.
func (d *Daemon) mintTurnToken(origin gate.Origin, taint gate.Taint, ttl time.Duration) string {
	tok := newID("tt")
	d.mu.Lock()
	d.turnTok[tok] = turnAuth{Origin: origin, Taint: taint, Expires: time.Now().Add(ttl)}
	d.mu.Unlock()
	return tok
}

func (d *Daemon) releaseTurnToken(tok string) {
	d.mu.Lock()
	delete(d.turnTok, tok)
	d.mu.Unlock()
}

func (d *Daemon) lookupTurnToken(tok string) (turnAuth, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	ta, ok := d.turnTok[tok]
	if !ok || time.Now().After(ta.Expires) {
		delete(d.turnTok, tok)
		return turnAuth{}, false
	}
	return ta, true
}

// stableSessionToken returns the daemon's one long-lived tool-proxy token,
// minting it on first use. It must not rotate per turn: the MCP bridge child
// a warm session spawns reads its --mcp-config policy file once at its own
// startup and keeps running across many turns, so a fresh token every turn
// would leave every call after the first presenting a token the daemon has
// already forgotten.
func (d *Daemon) stableSessionToken() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ensureSessionTokenLocked()
}

// ensureSessionTokenLocked mints the session token (Clean) if it does not
// exist yet. Both stableSessionToken and escalateTaint go through it, so a
// taint escalation that happens before any turn has built its tool policy —
// the first turn after a daemon start escalates before turnEnv runs — is
// applied to the token rather than silently dropped.
func (d *Daemon) ensureSessionTokenLocked() string {
	if d.sessionToken == "" {
		d.sessionToken = newID("tt")
		d.turnTok[d.sessionToken] = turnAuth{Origin: gate.P0, Taint: gate.Clean, Expires: time.Now().Add(365 * 24 * time.Hour)}
	}
	return d.sessionToken
}

// escalateTaint marks the stable session token tainted for the rest of the
// daemon's run once any turn's assembled context pulled in external content.
// It only ever escalates — a later clean turn does not un-taint a session
// that has already seen untrusted content — which is the conservative
// direction to err in given one token now serves every turn in a warm
// session's life rather than one token per turn.
func (d *Daemon) escalateTaint(tainted bool) {
	if !tainted {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tok := d.ensureSessionTokenLocked()
	if ta, ok := d.turnTok[tok]; ok {
		ta.Taint = gate.Tainted
		d.turnTok[tok] = ta
	}
}

// twinFunctions renders every manifest function (except level B, which is
// never callable) as a tools.TwinFunction, for the MCP bridge's tool list.
func (d *Daemon) twinFunctions() []tools.TwinFunction {
	var out []tools.TwinFunction
	for _, id := range d.cfg.Manifest.FunctionIDs() {
		f, _ := d.cfg.Manifest.Function(id)
		if f.Level == twins.B {
			continue
		}
		_, spec, ok := d.cfg.Registry.Lookup(id)
		if !ok {
			continue
		}
		schema, _ := json.Marshal(spec.Schema)
		out = append(out, tools.TwinFunction{ID: id, Tool: tools.TwinToolName(id), Description: spec.Description, Schema: schema})
	}
	return out
}

// TwinToolPolicy builds the tools.Policy for one turn: every manifest
// function, routed back to this daemon's own socket with a fresh, turn-scoped
// token.
func (d *Daemon) TwinToolPolicy() *tools.Policy {
	return &tools.Policy{
		Role:       "ceo",
		Twin:       d.twinFunctions(),
		TwinSocket: d.cfg.SocketPath,
		TwinToken:  d.stableSessionToken(),
	}
}

// handleToolInvoke is the model-tool bridge's only entry point: a
// "connector.function" call, authorized exactly as the turn that minted the
// bearer token was. An A-level call (or a tainted S-level one) is queued for
// approval rather than executed, per the spec: the model never runs an
// outward action inline.
func (d *Daemon) handleToolInvoke(w http.ResponseWriter, r *http.Request) {
	tok, ok := bearerToken(r)
	if !ok {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return
	}
	ta, ok := d.lookupTurnToken(tok)
	if !ok {
		http.Error(w, "invalid or expired turn token", http.StatusUnauthorized)
		return
	}
	var body struct {
		Function string         `json:"function"`
		Args     map[string]any `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Args == nil {
		body.Args = map[string]any{}
	}

	if f, ok := d.cfg.Manifest.Function(body.Function); ok && gate.NeedsEnvelope(f.Level, ta.Taint) {
		env, err := d.cfg.Approvals.Propose(r.Context(), approvals.Envelope{
			Action: body.Function, Payload: body.Args, Origin: string(ta.Origin), Risk: string(functionRisk(d.cfg.Registry, body.Function)),
		})
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]any{"status": "denied", "reason": err.Error()})
			return
		}
		d.notifyApprovalRequired(env)
		writeJSON(w, http.StatusOK, map[string]any{"status": "queued", "approval_id": env.ID})
		return
	}

	res, err := d.cfg.Gate.Invoke(r.Context(), gate.Call{Function: body.Function, Args: body.Args, Origin: ta.Origin, Taint: ta.Taint})
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "denied", "reason": err.Error()})
		return
	}
	// The result carries content written by someone else (mail, an invited
	// event, a shared doc): every later call this session makes must be
	// treated as tainted too, per escalateTaint's doc comment.
	d.escalateTaint(res.Untrusted)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "output": res.Output})
}

func functionRisk(reg *connectors.Registry, id string) connectors.Risk {
	_, spec, ok := reg.Lookup(id)
	if !ok {
		return connectors.RiskMedium
	}
	return spec.Risk
}

// Paths bundles the filesystem locations a daemon instance uses, all under
// one water home directory.
type Paths struct {
	Home string
}

func (p Paths) RunDir() string      { return filepath.Join(p.Home, "run") }
func (p Paths) SocketPath() string  { return filepath.Join(p.RunDir(), "water.sock") }
func (p Paths) LockPath() string    { return filepath.Join(p.RunDir(), "daemon.lock") }
func (p Paths) ClientsPath() string { return filepath.Join(p.RunDir(), "clients.json") }

// Listen prepares the daemon's socket: the run directory (0700), the
// single-instance flock, and a fresh 0600 Unix socket. The returned unlock
// releases the flock; callers defer it after a successful Listen.
func Listen(paths Paths) (net.Listener, func(), error) {
	if err := os.MkdirAll(paths.RunDir(), 0o700); err != nil {
		return nil, nil, err
	}
	unlock, err := lockFile(paths.LockPath())
	if err != nil {
		return nil, nil, err
	}
	sock := paths.SocketPath()
	_ = os.Remove(sock) // safe: we hold the single-instance lock
	l, err := net.Listen("unix", sock)
	if err != nil {
		unlock()
		return nil, nil, err
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		l.Close()
		unlock()
		return nil, nil, err
	}
	return l, unlock, nil
}

// baseEnv is the runtime.Env shared by every read of state (fast paths, the
// system prompt, GET /v1/state): no tool policy, since nothing here lets the
// model call a connector function.
func (d *Daemon) baseEnv() runtime.Env {
	env := runtime.Env{
		Manifest:  d.cfg.Manifest,
		Store:     d.cfg.Store,
		Approvals: d.cfg.Approvals,
		RoleMD:    d.cfg.RoleMD,
		Backend:   d.cfg.Backend,
		Warm:      d.cfg.Warm,
		// A fast path that pulls in external content (the morning brief)
		// escalates the session the same way a tainted model turn does.
		OnTaint: d.escalateTaint,
	}
	// Assigned only when non-nil: a nil *decisions.Trigger boxed into the
	// runtime.DecisionSource interface would be a non-nil interface holding
	// a nil pointer, which Env.Decisions != nil checks would miss.
	if d.cfg.Decisions != nil {
		env.Decisions = d.cfg.Decisions
	}
	return env
}

// turnEnv builds the runtime.Env for one turn, with the twin's tool policy
// (the stable session proxy token — see stableSessionToken).
func (d *Daemon) turnEnv() runtime.Env {
	env := d.baseEnv()
	env.Tools = d.TwinToolPolicy()
	return env
}
