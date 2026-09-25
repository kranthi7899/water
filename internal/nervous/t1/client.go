package t1

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Call is one proposed intent match: which intent Tier 1 thinks the
// utterance names, and the string-valued arguments it read off the
// utterance for that intent's slots. Args is nil/empty when the intent
// takes no arguments, or the model supplied none.
type Call struct {
	Intent string
	Args   map[string]string
}

// ErrTextReply is returned by Propose (wrapped, so errors.Is finds it) when
// the sidecar answered with plain text instead of calling a tool — a
// distinct "no proposal" outcome from a genuinely empty reply (ErrTextReply
// is not returned) or a transport/protocol failure (a different, unwrapped
// error). The Tier 1 adapter maps this to escalation reason "t1_text",
// separately from "t1_no_call" (zero tool calls, no text either) and
// "t1_multi_call" (more than one tool call) — see docs/slices/R.md §10.
// This is the "specific sentinel" choice R-18's task brief left up to the
// implementer for telling a text reply apart from a truly empty one, since
// the Client interface itself only returns ([]Call, error).
var ErrTextReply = errors.New("t1: sidecar replied with text instead of a tool call")

// Client proposes at most one Call for an utterance, given the tool
// declarations built from the currently active registry (t1.Declarations).
//
// Propose returns a nil/empty Call slice and a nil error for a reply with
// zero tool calls and no text content either (rare in practice), the full
// list of calls the reply actually contained otherwise (so the caller can
// tell "one" from "several" itself), ErrTextReply for a text-only reply,
// and any other error for a transport or protocol failure talking to the
// sidecar. A tool call naming something outside decls is still returned as
// a Call (with whatever intent id Propose could recover, or the raw wire
// name if it could not) rather than dropped — the Tier 1 adapter is what
// decides "unknown intent" by checking the returned intent against its own
// registry, since only it knows what's currently a valid target.
type Client interface {
	Propose(ctx context.Context, utterance string, decls []Decl) ([]Call, error)
}

// developerPrompt instructs FunctionGemma to answer only by calling a
// declared tool when the utterance clearly matches one, and never to invent
// an argument value that isn't present in the utterance — Tier 1's own
// grounding check (internal/nervous.TryTier1) is defense in depth on top of
// this instruction, never a substitute for it.
const developerPrompt = "You are a function-calling router. If, and only if, the user's message clearly matches one of the declared tools, call exactly that one tool with arguments taken verbatim from the message. Never call more than one tool. If nothing matches, or you are unsure, reply with plain text instead of calling a tool. Never invent an argument value that is not present in the message."

// functionCallEndTag closes FunctionGemma's own raw tool-call format,
// verbatim: "<start_function_call>call:NAME{args}<end_function_call>".
// Passed as an explicit request-level stop sequence (see chatRequest.Stop)
// so generation halts the instant the model finishes its one real call,
// instead of continuing to hallucinate further fabricated call/response
// blocks out to MaxTokens. Also used by parseRawFunctionCall below to
// recognize the end of a raw call when llama-server's own OpenAI-style
// tool_calls extraction doesn't fire for this model (see that function's
// doc comment).
const functionCallEndTag = "<end_function_call>"

// requestTimeout bounds one /v1/chat/completions round trip. Tier 1's own
// caller (a later task's cascade) applies a shorter end-to-end deadline
// (Design §11.4: 400ms) via ctx; this is just a backstop so a wedged
// sidecar can't hang a Propose call forever when no context deadline is
// set (as in this package's own tests).
const requestTimeout = 5 * time.Second

// httpClient is the loopback-only Tier 1 sidecar client, over llama-server's
// OpenAI-compatible /v1/chat/completions endpoint.
type httpClient struct {
	endpoint string
	http     *http.Client
}

// NewHTTP builds a Client bound to endpoint, which must resolve to a
// loopback host (127.0.0.1, ::1 or "localhost") — the FunctionGemma sidecar
// is a local-only process (docs/slices/R.md Design §1(a): internal/nervous/t1
// "may import net/http but not os/exec. Its endpoint is validated as
// loopback"). NewHTTP refuses to construct a client for anything else,
// before any request is ever made.
func NewHTTP(endpoint string) (Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("t1: parse endpoint %q: %w", endpoint, err)
	}
	if !isLoopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("t1: endpoint %q is not loopback; refusing to build a Tier 1 client for a non-local host", endpoint)
	}
	return &httpClient{
		endpoint: strings.TrimRight(endpoint, "/"),
		http:     &http.Client{Timeout: requestTimeout},
	}, nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ---- OpenAI-style /v1/chat/completions wire types ----

type chatRequest struct {
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
	ToolChoice  string        `json:"tool_choice"`
	Tools       []wireTool    `json:"tools"`
	Stop        []string      `json:"stop,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type wireTool struct {
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}

type wireFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  wireParameters `json:"parameters"`
}

type wireParameters struct {
	Type       string                  `json:"type"`
	Properties map[string]wireProperty `json:"properties"`
	Required   []string                `json:"required,omitempty"`
}

type wireProperty struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content   string         `json:"content"`
			ToolCalls []wireToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
}

type wireToolCall struct {
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"` // a JSON-object-encoded string, per the OpenAI tool-calling wire format
	} `json:"function"`
}

func buildTools(decls []Decl) []wireTool {
	tools := make([]wireTool, 0, len(decls))
	for _, d := range decls {
		props := make(map[string]wireProperty, len(d.Params))
		var required []string
		for name, p := range d.Params {
			props[name] = wireProperty{Type: p.Type, Description: p.Description}
			if p.Required {
				required = append(required, name)
			}
		}
		sort.Strings(required)
		tools = append(tools, wireTool{
			Type: "function",
			Function: wireFunction{
				Name:        d.Name,
				Description: d.Description,
				Parameters:  wireParameters{Type: "object", Properties: props, Required: required},
			},
		})
	}
	return tools
}

// Propose sends one request to the sidecar and parses its reply. See the
// Client interface's doc comment for exactly what each outcome returns.
func (c *httpClient) Propose(ctx context.Context, utterance string, decls []Decl) ([]Call, error) {
	reqBody := chatRequest{
		Messages: []chatMessage{
			{Role: "system", Content: developerPrompt},
			{Role: "user", Content: utterance},
		},
		Temperature: 0,
		MaxTokens:   128,
		ToolChoice:  "auto",
		Tools:       buildTools(decls),
		// Without an explicit stop sequence, FunctionGemma correctly emits
		// one <start_function_call>...<end_function_call> block and then,
		// rather than stopping, keeps hallucinating further fabricated
		// "call"/"function_response" blocks until it exhausts MaxTokens
		// (finish_reason "length", ~128 tokens every time) -- harmless to
		// correctness (the parser below only ever looks at the first real
		// call and multi-call already maps to t1_multi_call), but it was
		// the dominant cost in Slice R's live warm_p95_ms measurement
		// (1313ms, failing the 400ms eval-gate threshold). Measured
		// directly against the real sidecar: adding this stop sequence
		// drops p95 to ~165ms (finish_reason "stop", ~16 tokens) with no
		// change in which case answers or how. See
		// docs/slices/R-verification.md and docs/known-gaps.md.
		Stop: []string{functionCallEndTag},
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("t1: marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("t1: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("t1: request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("t1: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("t1: unexpected status %s from sidecar: %s", resp.Status, truncate(string(respBody), 200))
	}

	return parseResponse(respBody, decls)
}

func parseResponse(b []byte, decls []Decl) ([]Call, error) {
	var cr chatResponse
	if err := json.Unmarshal(b, &cr); err != nil {
		return nil, fmt.Errorf("t1: parse response: %w", err)
	}
	if len(cr.Choices) == 0 {
		return nil, nil
	}
	msg := cr.Choices[0].Message

	byName := make(map[string]string, len(decls))
	for _, d := range decls {
		byName[d.Name] = d.intentID
	}

	if len(msg.ToolCalls) == 0 {
		// llama-server's OpenAI-style structured tool_calls extraction does
		// not fire for FunctionGemma today (checked directly against the
		// real sidecar during Slice R's Phase 4 verification): the model
		// correctly emits its own raw
		// "<start_function_call>call:NAME{args}<end_function_call>" format
		// in message.content, but nothing populates the wire tool_calls
		// field from it, so every real answer was previously
		// misclassified as ErrTextReply -- Tier 1 could structurally never
		// answer anything. rawFunctionCalls parses that format directly, so
		// this stays correct even if a future llama-server version starts
		// populating tool_calls natively for this model (that path is
		// tried first, above, and unaffected).
		raw, ok := rawFunctionCalls(msg.Content)
		if !ok {
			if strings.TrimSpace(msg.Content) != "" {
				return nil, ErrTextReply
			}
			return nil, nil
		}
		calls := make([]Call, 0, len(raw))
		for _, rc := range raw {
			calls = append(calls, toCall(rc.name, rc.args, byName))
		}
		return calls, nil
	}

	calls := make([]Call, 0, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		args, err := parseArguments(tc.Function.Arguments)
		if err != nil {
			return nil, fmt.Errorf("t1: parse tool call arguments: %w", err)
		}
		calls = append(calls, toCall(tc.Function.Name, args, byName))
	}
	return calls, nil
}

// toCall maps a wire function name to the registry intent id byName
// declared it under. An unrecognized name is passed through unconverted:
// it cannot collide with a real registry id (every real id contains a
// "."; a wire name never does), so the Tier 1 adapter's own registry
// lookup will always and correctly treat this as t1_unknown_intent.
func toCall(name string, args map[string]string, byName map[string]string) Call {
	intentID, known := byName[name]
	if !known {
		intentID = name
	}
	return Call{Intent: intentID, Args: args}
}

// rawCall is one function call extracted directly from FunctionGemma's own
// text format, before it is mapped to a registry intent id.
type rawCall struct {
	name string
	args map[string]string
}

// startCallTag opens FunctionGemma's raw tool-call format (functionCallEndTag
// closes it, see that constant's own doc comment).
const startCallTag = "<start_function_call>"

// rawCallRe matches one "call:NAME{args}" block. args is captured
// non-greedily up to the first "}" that is not itself inside an
// <escape>...</escape>-wrapped string value (rawArgRe below re-splits the
// captured body properly; this outer match only needs to find the block's
// boundaries, and real argument values never contain a literal
// "<start_function_call>", so scanning block-by-block on that marker,
// below, keeps this simple and correct for the layouts actually observed
// against the real model).
var rawCallRe = regexp.MustCompile(`^call:([a-zA-Z_][a-zA-Z0-9_]*)\{(.*)\}\s*$`)

// rawArgRe matches one key:value pair inside a call's argument body: either
// key:<escape>...<escape> (a string value, which may itself contain commas
// or braces) or key:token (a bare, unescaped value -- a number or a short
// unquoted word, per the raw examples observed against the real model).
var rawArgRe = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*):(?:<escape>(.*?)<escape>|([^,}]*))`)

// rawFunctionCalls extracts every "<start_function_call>call:NAME{args}"
// block from content (FunctionGemma's own raw output format), stopping
// each block at the next functionCallEndTag or the next startCallTag,
// whichever comes first, so a hallucinated run of extra fabricated calls
// (Design's known failure mode without an explicit stop sequence, see
// chatRequest.Stop) still yields one rawCall per block rather than one
// giant unparseable blob. ok is false only when content contains no
// startCallTag at all -- a genuine plain-text reply, callers' existing
// ErrTextReply case.
func rawFunctionCalls(content string) (calls []rawCall, ok bool) {
	segments := strings.Split(content, startCallTag)
	if len(segments) < 2 {
		return nil, false
	}
	for _, seg := range segments[1:] {
		if i := strings.Index(seg, functionCallEndTag); i >= 0 {
			seg = seg[:i]
		}
		seg = strings.TrimSpace(seg)
		m := rawCallRe.FindStringSubmatch(seg)
		if m == nil {
			continue
		}
		args := map[string]string{}
		for _, am := range rawArgRe.FindAllStringSubmatch(m[2], -1) {
			key := am[1]
			// Go's regexp alternation tries the <escape>...<escape> branch
			// first; it wins whenever it can match at all (well-formed
			// escaped values always have both tags), so its presence in
			// the full match text (am[0]) reliably tells the two branches
			// apart, including the empty-string case (<escape><escape>).
			if strings.Contains(am[0], "<escape>") {
				args[key] = am[2]
			} else {
				args[key] = strings.TrimSpace(am[3])
			}
		}
		calls = append(calls, rawCall{name: m[1], args: args})
	}
	return calls, true
}

// parseArguments decodes a tool call's Arguments string (a JSON object
// encoded as a string, per the OpenAI wire format) into a plain
// map[string]string. A JSON number or boolean value is stringified (a
// count slot may come back as a bare JSON number rather than a quoted
// string); anything else (nested object/array) is rejected rather than
// silently stringified into something misleading.
func parseArguments(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return nil, fmt.Errorf("arguments %q: %w", raw, err)
	}
	out := make(map[string]string, len(decoded))
	for k, v := range decoded {
		s, err := stringifyArg(v)
		if err != nil {
			return nil, fmt.Errorf("argument %q: %w", k, err)
		}
		out[k] = s
	}
	return out, nil
}

func stringifyArg(v any) (string, error) {
	switch x := v.(type) {
	case string:
		return x, nil
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10), nil
		}
		return strconv.FormatFloat(x, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(x), nil
	case nil:
		return "", nil
	default:
		return "", fmt.Errorf("unsupported argument value type %T", v)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
