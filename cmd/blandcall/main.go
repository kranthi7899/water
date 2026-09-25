// Command blandcall places a small, hard-capped number of real phone calls
// through Bland AI's call API, for one scoped purpose: demonstrating Water's
// voice capability live (e.g. to a recruiter).
//
// This is deliberately NOT a water connector. It is never registered in
// buildCEORegistry, never listed in twins/ceo/twin.yaml, and never goes
// through internal/gate — so it never touches the invariant the rest of
// this codebase holds everywhere else ("nothing may send, post, call,
// delete or spend without a passing gate check and an approved envelope",
// CLAUDE.md). Placing a real, metered phone call is itself an explicit,
// user-approved, out-of-band exception to this project's zero-metered-spend
// rule — enforced here by a local counter (~/.water/blandcall/state.json)
// against the maxCalls cap below, not by discipline alone. The cap started
// at 3 and was explicitly raised to 5 by the owner in the conversation that
// built this tool (see docs/water-context-primer.md and the session log;
// as of this comment neither CLAUDE.md nor docs/EVOLUTION_PLAN.md records
// the exception itself, which a future session should not treat as silent
// permission to raise it further without asking again). It exists to be run
// directly from a terminal, by hand, not to be extended into a fourth
// connector, and a future session should not wire this into the gated
// product without the same explicit, deliberate approval this one got.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"water/internal/vault"
)

const (
	service  = "water.bland-demo"
	account  = "default"
	baseURL  = "https://api.bland.ai"
	maxCalls = 5
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "connect":
		err = cmdConnect(os.Args[2:])
	case "call":
		err = cmdCall(os.Args[2:])
	case "status":
		err = cmdStatus(os.Args[2:])
	case "budget":
		err = cmdBudget()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "blandcall:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `blandcall: a standalone, out-of-band tool for placing up to 3 Bland AI
phone calls. Not part of the water daemon or its gate — see the doc
comment at the top of cmd/blandcall/main.go before touching this.

Usage:
  blandcall connect --token <bland API key>
  blandcall call --to <+15551234567> --pointers "what to convey"
                 [--wait-for-greeting=true] [--voice Karen] [--max-duration 5]
                 [--retry-after 5m] [--dry-run] [--no-poll]
  blandcall status --id <call_id>
  blandcall budget
`)
}

// state is the local, code-enforced call budget — maxCalls, total, forever,
// for this tool. It lives outside the vault (it holds no secret) at
// ~/.water/blandcall/state.json.
type state struct {
	CallsMade int          `json:"calls_made"`
	History   []callRecord `json:"history"`
}

type callRecord struct {
	CallID string    `json:"call_id"`
	To     string    `json:"to"`
	At     time.Time `json:"at"`
}

func statePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".water", "blandcall")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

func loadState() (state, error) {
	p, err := statePath()
	if err != nil {
		return state{}, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return state{}, nil
	}
	if err != nil {
		return state{}, err
	}
	var s state
	if err := json.Unmarshal(b, &s); err != nil {
		return state{}, err
	}
	return s, nil
}

func saveState(s state) error {
	p, err := statePath()
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

func cmdBudget() error {
	s, err := loadState()
	if err != nil {
		return err
	}
	fmt.Printf("calls made: %d/%d\n", s.CallsMade, maxCalls)
	for _, r := range s.History {
		fmt.Printf("  %s  %s  call_id=%s\n", r.At.Format(time.RFC3339), r.To, r.CallID)
	}
	return nil
}

func cmdConnect(args []string) error {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	token := fs.String("token", "", "Bland AI API key")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *token == "" {
		return errors.New("--token is required")
	}
	return vault.Default().Set(service, account, vault.NewSecret(*token))
}

// basePersona is prepended to every call's --pointers. It intentionally
// contains nothing about what any particular call is *for* — only how to
// behave on any call — so it never fights with --pointers' objective. An
// earlier version of this constant hardcoded one call's own goals (ask
// about a demo video, request feedback) as a fixed numbered script; that
// leaked into every other call regardless of its real purpose, which is
// likely why a call about something unrelated still felt like it was
// reciting demo-video talking points. --pointers now supplies the entire
// objective and rubric; this constant only supplies the behavior.
const basePersona = `You are Water, an AI assistant twin built by Kranthi, placing a live phone call on his behalf. This is a real, live conversation: you can hear the other person's actual words in real time, and every reply you give should respond specifically to what they just said — never march through a fixed script regardless of their answers. Keep your own turns short, a sentence or two, the way a real phone call actually sounds, not a monologue or a recitation.

Below is this call's objective and a rough rubric of things worth covering if the conversation naturally allows it. Treat the rubric as a guide to what matters, not a script of lines to read in a fixed order — skip, reorder, adapt or drop items based on how the person actually responds. If they say something unexpected, engage with it genuinely before returning to the objective.`

func cmdCall(args []string) error {
	fs := flag.NewFlagSet("call", flag.ExitOnError)
	to := fs.String("to", "", "recipient phone number, E.164 format (e.g. +15551234567)")
	pointers := fs.String("pointers", "", "this call's objective and rubric (what to accomplish, what's worth covering) — not a script; see the basePersona doc comment")
	pointersFile := fs.String("pointers-file", "", "read --pointers from this file instead (avoids fragile multi-line shell quoting); mutually exclusive with --pointers")
	waitForGreeting := fs.Bool("wait-for-greeting", true, "wait for the recipient to speak first")
	voice := fs.String("voice", "Karen", "Bland voice name")
	maxDuration := fs.Int("max-duration", 5, "max call length in minutes")
	retryAfter := fs.Duration("retry-after", 0, "if unanswered/voicemail, wait this long and place exactly one retry (still counts against the 3-call budget); 0 disables")
	dryRun := fs.Bool("dry-run", false, "print the request that would be sent; do not call, do not spend budget")
	noPoll := fs.Bool("no-poll", false, "do not poll for the call's outcome after placing it")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *pointersFile != "" {
		if *pointers != "" {
			return errors.New("--pointers and --pointers-file are mutually exclusive")
		}
		b, err := os.ReadFile(*pointersFile)
		if err != nil {
			return fmt.Errorf("reading --pointers-file: %w", err)
		}
		*pointers = string(b)
	}
	if *to == "" || *pointers == "" {
		return errors.New("--to and (--pointers or --pointers-file) are required")
	}

	s, err := loadState()
	if err != nil {
		return err
	}
	if !*dryRun && s.CallsMade >= maxCalls {
		return fmt.Errorf("call budget exhausted: %d/%d calls already made (see `blandcall budget`); this tool refuses a call beyond the cap by design", s.CallsMade, maxCalls)
	}

	task := basePersona + "\n\n" + *pointers
	body := map[string]any{
		"phone_number":      *to,
		"task":              task,
		"voice":             *voice,
		"wait_for_greeting": *waitForGreeting,
		"max_duration":      *maxDuration,
		"record":            false,
		"voicemail":         map[string]any{"action": "hangup"},
	}

	if *dryRun {
		b, _ := json.MarshalIndent(body, "", "  ")
		fmt.Println(string(b))
		fmt.Printf("\n(dry run: not sent, budget untouched — %d/%d calls made so far, no token needed)\n", s.CallsMade, maxCalls)
		return nil
	}

	secret, err := vault.Default().Get(service, account)
	if err != nil {
		return fmt.Errorf("not connected; run `blandcall connect --token <key>` first: %w", err)
	}

	callID, err := placeCall(secret.Reveal(), body)
	if err != nil {
		return err
	}
	fmt.Println("call placed:", callID)

	s.CallsMade++
	s.History = append(s.History, callRecord{CallID: callID, To: *to, At: time.Now().UTC()})
	if err := saveState(s); err != nil {
		return fmt.Errorf("call placed (%s) but saving the local budget counter failed, fix this before calling again: %w", callID, err)
	}
	fmt.Printf("budget: %d/%d calls made\n", s.CallsMade, maxCalls)

	if *noPoll {
		return nil
	}
	outcome, err := pollCall(secret.Reveal(), callID, time.Duration(*maxDuration+1)*time.Minute)
	if err != nil {
		fmt.Fprintln(os.Stderr, "poll:", err)
		return nil
	}
	printOutcome(outcome)

	if *retryAfter > 0 && !outcome.reachedHuman() {
		if s.CallsMade >= maxCalls {
			fmt.Printf("not answered, but the %d-call budget is used up — no retry.\n", maxCalls)
			return nil
		}
		fmt.Printf("not answered (answered_by=%q); waiting %s before one retry...\n", outcome.AnsweredBy, *retryAfter)
		time.Sleep(*retryAfter)
		retryID, err := placeCall(secret.Reveal(), body)
		if err != nil {
			return fmt.Errorf("retry failed: %w", err)
		}
		fmt.Println("retry call placed:", retryID)
		s.CallsMade++
		s.History = append(s.History, callRecord{CallID: retryID, To: *to, At: time.Now().UTC()})
		if err := saveState(s); err != nil {
			return fmt.Errorf("retry placed (%s) but saving the local budget counter failed: %w", retryID, err)
		}
		fmt.Printf("budget: %d/%d calls made\n", s.CallsMade, maxCalls)
		if !*noPoll {
			retryOutcome, err := pollCall(secret.Reveal(), retryID, time.Duration(*maxDuration+1)*time.Minute)
			if err != nil {
				fmt.Fprintln(os.Stderr, "poll:", err)
				return nil
			}
			printOutcome(retryOutcome)
		}
	}
	return nil
}

func placeCall(token string, body map[string]any) (string, error) {
	b, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/calls", bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("bland: %s: %s", resp.Status, string(respBody))
	}
	var out struct {
		CallID string `json:"call_id"`
	}
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", fmt.Errorf("decoding bland response: %w (body: %s)", err, string(respBody))
	}
	if out.CallID == "" {
		return "", fmt.Errorf("bland accepted the request but returned no call_id: %s", string(respBody))
	}
	return out.CallID, nil
}

type callStatus struct {
	Completed    bool    `json:"completed"`
	Status       string  `json:"status"`
	AnsweredBy   string  `json:"answered_by"`
	CallLength   float64 `json:"call_length"`
	ErrorMessage string  `json:"error_message"`
}

func (c callStatus) reachedHuman() bool {
	return c.AnsweredBy == "human" || c.AnsweredBy == "unknown"
}

func getCallStatus(token, callID string) (callStatus, error) {
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/calls/"+callID, nil)
	if err != nil {
		return callStatus{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return callStatus{}, err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return callStatus{}, fmt.Errorf("bland: %s: %s", resp.Status, string(respBody))
	}
	var out callStatus
	if err := json.Unmarshal(respBody, &out); err != nil {
		return callStatus{}, fmt.Errorf("decoding bland status: %w (body: %s)", err, string(respBody))
	}
	return out, nil
}

// pollCall checks the call's status every 10s until it completes or timeout
// elapses, and returns whatever the last poll saw either way.
func pollCall(token, callID string, timeout time.Duration) (callStatus, error) {
	deadline := time.Now().Add(timeout)
	var last callStatus
	for time.Now().Before(deadline) {
		s, err := getCallStatus(token, callID)
		if err != nil {
			return last, err
		}
		last = s
		if s.Completed {
			return s, nil
		}
		time.Sleep(10 * time.Second)
	}
	return last, fmt.Errorf("call %s did not complete within %s (last status: %+v)", callID, timeout, last)
}

func printOutcome(s callStatus) {
	fmt.Printf("outcome: status=%s answered_by=%s call_length=%.1fmin", s.Status, s.AnsweredBy, s.CallLength)
	if s.ErrorMessage != "" {
		fmt.Printf(" error=%q", s.ErrorMessage)
	}
	fmt.Println()
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	id := fs.String("id", "", "call id")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *id == "" {
		return errors.New("--id is required")
	}
	secret, err := vault.Default().Get(service, account)
	if err != nil {
		return fmt.Errorf("not connected; run `blandcall connect --token <key>` first: %w", err)
	}
	s, err := getCallStatus(secret.Reveal(), *id)
	if err != nil {
		return err
	}
	printOutcome(s)
	return nil
}
