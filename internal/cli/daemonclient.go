package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"water/internal/approvals"
	"water/internal/config"
	"water/internal/gateway"
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

func newDaemonClient() (*daemonClient, error) {
	paths := gateway.Paths{Home: config.Home()}
	sock := paths.SocketPath()
	if _, err := os.Stat(sock); err != nil {
		return nil, errDaemonNotRunning
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

// DecisionResult mirrors gateway.DecisionResult: the envelope's final state,
// and — when the answer was yes — whether the action actually ran.
type DecisionResult struct {
	Envelope approvals.Envelope `json:"envelope"`
	Executed bool               `json:"executed"`
	Output   json.RawMessage    `json:"output,omitempty"`
	Error    string             `json:"error,omitempty"`
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
