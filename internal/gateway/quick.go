package gateway

import (
	"encoding/json"
	"net/http"

	"water/internal/runtime"
)

// handleQuickInvoke serves the sous chef's quick.* tools to the main
// agent's MCP bridge: a fixed, read-only reflex.Table() lookup, resolved
// and run entirely inside this process. It is authenticated exactly like
// /v1/tools/invoke (the same session tool-proxy token), but it never reads
// d.cfg.Gate, d.cfg.Registry or d.cfg.Approvals directly, and never calls
// Invoke on anything gate-shaped — see quick_boundary_test.go, which parses
// this file and fails if that ever stops being true. The only path from a
// quick.* call to an outward action is: there isn't one. Every handler
// nervous.Quick() can reach is read-only by construction (Design §1,
// reflex/imports_test.go).
func (d *Daemon) handleQuickInvoke(w http.ResponseWriter, r *http.Request) {
	tok, ok := bearerToken(r)
	if !ok {
		http.Error(w, "missing bearer token", http.StatusUnauthorized)
		return
	}
	if _, ok := d.lookupTurnToken(tok); !ok {
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
	// The call is a step on the active turn's stream (V-events), labelled
	// from the quick tools' own table, never from the arguments; an id that
	// table doesn't expose gets no tool name and the generic label. Every
	// return path below ends it through the defer.
	tool, label := "", genericStepLabel
	if d.cfg.Nervous != nil {
		if l, ok := d.cfg.Nervous.Quick().Activity(body.Function); ok {
			tool, label = body.Function, l
		}
	}
	step := d.beginStep(tool, label)
	status := runtime.StepError
	defer func() { step.end(status) }()
	if d.cfg.Nervous == nil {
		status = runtime.StepDenied
		writeJSON(w, http.StatusOK, map[string]any{"status": "denied", "reason": "no quick tools are configured for this twin"})
		return
	}

	res, err := d.cfg.Nervous.Quick().Run(r.Context(), body.Function, body.Args)
	if err != nil {
		status = runtime.StepDenied
		writeJSON(w, http.StatusOK, map[string]any{"status": "denied", "reason": err.Error()})
		return
	}
	d.escalateTaint(res.Tainted)
	d.cfg.Nervous.RecordToolUse(body.Function)
	status = runtime.StepOK
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "output": res.Output})
}
