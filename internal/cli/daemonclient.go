package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"water/internal/approvals"
	"water/internal/config"
	"water/internal/decisions"
	"water/internal/gateway"
	"water/internal/nervous"
	"water/internal/nervous/promote"
	"water/internal/runtime"
)

// daemonClient talks to a running water daemon over its Unix socket. Every
// daemon client says how to start it if the socket is missing; none of them
// falls back to running in-process.
type daemonClient struct {
	http  *http.Client
	token string
}

var errDaemonNotRunning = fmt.Errorf("water daemon is not running. Start it with `water daemon` (or install it to start automatically: `water daemon install`)")

// daemonLiveness is what probeDaemon learned about the daemon's socket.
type daemonLiveness int

const (
	daemonAbsent daemonLiveness = iota // no socket file
	daemonStale                        // a socket file is there but nothing answers (a crash or SIGKILL left it)
	daemonUp                           // something accepted a connection on the socket
)

// probeDaemon decides whether the daemon is running by dialling its socket,
// not by checking that the socket file exists: a daemon that was SIGKILLed
// or crashed never runs its shutdown, so the file outlives it.
func probeDaemon(sock string) daemonLiveness {
	if _, err := os.Stat(sock); err != nil {
		return daemonAbsent
	}
	c, err := net.DialTimeout("unix", sock, 500*time.Millisecond)
	if err != nil {
		return daemonStale
	}
	_ = c.Close()
	return daemonUp
}

func newDaemonClient() (*daemonClient, error) {
	paths := gateway.Paths{Home: config.Home()}
	sock := paths.SocketPath()
	switch probeDaemon(sock) {
	case daemonAbsent:
		return nil, errDaemonNotRunning
	case daemonStale:
		return nil, fmt.Errorf("%w (stale socket at %s: nothing is listening)", errDaemonNotRunning, sock)
	}
	clients, err := gateway.LoadClients(paths.ClientsPath())
	if err != nil {
		return nil, err
	}
	tok, err := clients.EnsureCLI()
	if err != nil {
		return nil, err
	}
	hc := &http.Client{
		Timeout: 5 * time.Minute,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		},
	}
	return &daemonClient{http: hc, token: tok}, nil
}

func (c *daemonClient) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://water"+path, r)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// A daemon that died mid-session gets the same start-it hint as one
		// that was never running.
		if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ENOENT) {
			return nil, fmt.Errorf("%w (%v)", errDaemonNotRunning, err)
		}
		return nil, fmt.Errorf("water daemon: %w", err)
	}
	return resp, nil
}

func httpError(resp *http.Response) error {
	b, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("water daemon: %s: %s", resp.Status, strings.TrimSpace(string(b)))
}

// Turn streams one turn, calling onEvent for each event in order. clear
// resets the warm session's context before this turn runs (chat's /clear).
func (c *daemonClient) Turn(ctx context.Context, channel, prompt string, clear bool, onEvent func(runtime.Event)) error {
	resp, err := c.do(ctx, http.MethodPost, "/v1/turns", map[string]any{"channel": channel, "prompt": prompt, "clear": clear})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return httpError(resp)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 4<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e runtime.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		onEvent(e)
	}
	return sc.Err()
}

// Pending lists approvals waiting for a decision.
func (c *daemonClient) Pending(ctx context.Context) ([]approvals.Envelope, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/approvals", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, httpError(resp)
	}
	var envs []approvals.Envelope
	if err := json.NewDecoder(resp.Body).Decode(&envs); err != nil {
		return nil, err
	}
	return envs, nil
}

// Decisions runs today's classification-trigger orchestration on the daemon
// and returns every card it built, already ranked by severity then deadline
// (decisions.Rank).
func (c *daemonClient) Decisions(ctx context.Context) ([]*decisions.Card, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/decisions", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, httpError(resp)
	}
	var cards []*decisions.Card
	if err := json.NewDecoder(resp.Body).Decode(&cards); err != nil {
		return nil, err
	}
	return cards, nil
}

// EmailDecisionResult mirrors what POST /v1/decisions/{id}/email answers:
// either the card was found and a send_message approval was queued for it
// (Status "queued", ApprovalID set), or it was refused (Status "denied",
// Reason set) before anything was proposed.
type EmailDecisionResult struct {
	Status     string `json:"status"`
	ApprovalID string `json:"approval_id,omitempty"`
	Reason     string `json:"reason,omitempty"`
}

// EmailDecisionReport asks the daemon to render decision card id as an HTML
// report (internal/reports, via Card.HTMLReport) and stage it as a
// gmail.send_message approval, with the report as html_attachment. Nothing
// is sent until the CEO decides the resulting approval, exactly like any
// other level-A action (`water approve`).
func (c *daemonClient) EmailDecisionReport(ctx context.Context, id string, to []string, subject, body string) (EmailDecisionResult, error) {
	resp, err := c.do(ctx, http.MethodPost, "/v1/decisions/"+id+"/email", map[string]any{"to": to, "subject": subject, "body": body})
	if err != nil {
		return EmailDecisionResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return EmailDecisionResult{}, httpError(resp)
	}
	var out EmailDecisionResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return EmailDecisionResult{}, err
	}
	return out, nil
}

// DecisionResult mirrors gateway.DecisionResult: the envelope's final state,
// and — when the answer was yes — whether the action actually ran.
type DecisionResult struct {
	Envelope       approvals.Envelope `json:"envelope"`
	Executed       bool               `json:"executed"`
	Output         json.RawMessage    `json:"output,omitempty"`
	Error          string             `json:"error,omitempty"`
	OutcomeUnknown bool               `json:"outcome_unknown,omitempty"`
}

// Decide answers a pending approval. reply is matched with the same
// deterministic yes/no matcher the voice channel uses. A "yes" both approves
// and (in one call) executes the action exactly once.
func (c *daemonClient) Decide(ctx context.Context, id, payloadHash, reply string) (DecisionResult, error) {
	resp, err := c.do(ctx, http.MethodPost, "/v1/approvals/"+id+"/decision", map[string]string{"payload_hash": payloadHash, "reply": reply})
	if err != nil {
		return DecisionResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return DecisionResult{}, httpError(resp)
	}
	var out DecisionResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return DecisionResult{}, err
	}
	return out, nil
}

// RouteCandidates lists repeated main-agent quick-tool-usage patterns that
// could become a learned Tier 0 intent (Design §16's growth loop, detection
// only — R-22). Always available: GET /v1/route/candidates is never gated
// on router.promotion.enabled.
func (c *daemonClient) RouteCandidates(ctx context.Context, since time.Duration, minRepeats int) ([]promote.Candidate, error) {
	q := url.Values{}
	if since > 0 {
		q.Set("since", since.String())
	}
	if minRepeats > 0 {
		q.Set("min", strconv.Itoa(minRepeats))
	}
	path := "/v1/route/candidates"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, httpError(resp)
	}
	var cands []promote.Candidate
	if err := json.NewDecoder(resp.Body).Decode(&cands); err != nil {
		return nil, err
	}
	return cands, nil
}

// RouteReport summarizes recent route_log rows (tier distribution,
// escalation reasons, latency percentiles, possible misses) — `water route
// report` (R-26), GET /v1/route/report (R-14).
func (c *daemonClient) RouteReport(ctx context.Context, since time.Duration) (nervous.Report, error) {
	q := url.Values{}
	if since > 0 {
		q.Set("since", since.String())
	}
	path := "/v1/route/report"
	if enc := q.Encode(); enc != "" {
		path += "?" + enc
	}
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nervous.Report{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nervous.Report{}, httpError(resp)
	}
	var rep nervous.Report
	if err := json.NewDecoder(resp.Body).Decode(&rep); err != nil {
		return nervous.Report{}, err
	}
	return rep, nil
}

// RouterHealth mirrors GET /v1/router's JSON: Tier 0's breaker state, every
// intent the registry knows about but currently cannot answer from (and
// why), every learned overlay file that failed to load, and whether the
// promotion loop is enabled (router.promotion.enabled) — the one field
// `water intent promote` checks before ever prompting for confirmation.
type RouterHealth struct {
	Tier0 struct {
		State  string    `json:"state"`
		Reason string    `json:"reason"`
		Since  time.Time `json:"since"`
	} `json:"tier0"`
	InactiveIntents []struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	} `json:"inactive_intents"`
	LearnedSkipped []struct {
		File   string `json:"file"`
		Reason string `json:"reason"`
	} `json:"learned_skipped"`
	PromotionEnabled bool `json:"promotion_enabled"`
}

// RouterHealth calls GET /v1/router.
func (c *daemonClient) RouterHealth(ctx context.Context) (RouterHealth, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/router", nil)
	if err != nil {
		return RouterHealth{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return RouterHealth{}, httpError(resp)
	}
	var h RouterHealth
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return RouterHealth{}, err
	}
	return h, nil
}

// VoiceProfileResult mirrors GET /v1/voice/profile's JSON (R-15): the
// twin's name, handoff phrases and TTS voice/rate, so every client's voice
// output matches regardless of which tier answered.
type VoiceProfileResult struct {
	Name    string   `json:"name"`
	Handoff []string `json:"handoff"`
	TTS     struct {
		Voice   string `json:"voice"`
		RateWPM int    `json:"rate_wpm"`
	} `json:"tts"`
}

// VoiceProfile calls GET /v1/voice/profile.
func (c *daemonClient) VoiceProfile(ctx context.Context) (VoiceProfileResult, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/voice/profile", nil)
	if err != nil {
		return VoiceProfileResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return VoiceProfileResult{}, httpError(resp)
	}
	var v VoiceProfileResult
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return VoiceProfileResult{}, err
	}
	return v, nil
}

// IntentListItem is one row of GET /v1/intents: enough to tell an embedded
// intent from a learned one, and to say exactly why an inactive or disabled
// one currently cannot answer.
type IntentListItem struct {
	ID             string `json:"id"`
	Origin         string `json:"origin"`
	Active         bool   `json:"active"`
	InactiveReason string `json:"inactive_reason,omitempty"`
	Disabled       string `json:"disabled,omitempty"`
}

// IntentList calls GET /v1/intents.
func (c *daemonClient) IntentList(ctx context.Context) ([]IntentListItem, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/intents", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, httpError(resp)
	}
	var items []IntentListItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, err
	}
	return items, nil
}

// IntentDraftResult mirrors POST /v1/intents/draft's response.
type IntentDraftResult struct {
	ID              string `json:"id"`
	YAML            string `json:"yaml"`
	Path            string `json:"path"`
	Valid           bool   `json:"valid"`
	ValidationError string `json:"validation_error,omitempty"`
}

// IntentDraft calls POST /v1/intents/draft for candidateID.
func (c *daemonClient) IntentDraft(ctx context.Context, candidateID string) (IntentDraftResult, error) {
	resp, err := c.do(ctx, http.MethodPost, "/v1/intents/draft", map[string]string{"candidate_id": candidateID})
	if err != nil {
		return IntentDraftResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return IntentDraftResult{}, httpError(resp)
	}
	var out IntentDraftResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return IntentDraftResult{}, err
	}
	return out, nil
}

// IntentPromoteResult mirrors POST /v1/intents/promote's response.
type IntentPromoteResult struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Reloaded bool   `json:"reloaded"`
}

// IntentPromote calls POST /v1/intents/promote for candidateID: the daemon
// re-reads and re-validates the pending file `water intent draft` wrote,
// writes it into the learned overlay only on a passing validation, and
// reloads the live registry.
func (c *daemonClient) IntentPromote(ctx context.Context, candidateID string) (IntentPromoteResult, error) {
	resp, err := c.do(ctx, http.MethodPost, "/v1/intents/promote", map[string]string{"candidate_id": candidateID})
	if err != nil {
		return IntentPromoteResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return IntentPromoteResult{}, httpError(resp)
	}
	var out IntentPromoteResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return IntentPromoteResult{}, err
	}
	return out, nil
}

// IntentStateResult mirrors POST /v1/intents/demote|enable's response.
type IntentStateResult struct {
	ID       string `json:"id"`
	Disabled bool   `json:"disabled"`
	Reloaded bool   `json:"reloaded"`
}

// IntentDemote calls POST /v1/intents/demote, disabling id.
func (c *daemonClient) IntentDemote(ctx context.Context, id, reason string) (IntentStateResult, error) {
	return c.intentState(ctx, "/v1/intents/demote", id, reason)
}

// IntentEnable calls POST /v1/intents/enable, re-enabling id.
func (c *daemonClient) IntentEnable(ctx context.Context, id string) (IntentStateResult, error) {
	return c.intentState(ctx, "/v1/intents/enable", id, "")
}

func (c *daemonClient) intentState(ctx context.Context, path, id, reason string) (IntentStateResult, error) {
	resp, err := c.do(ctx, http.MethodPost, path, map[string]string{"id": id, "reason": reason})
	if err != nil {
		return IntentStateResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return IntentStateResult{}, httpError(resp)
	}
	var out IntentStateResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return IntentStateResult{}, err
	}
	return out, nil
}
