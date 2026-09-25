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
	if len(msg.ToolCalls) == 0 {
		if strings.TrimSpace(msg.Content) != "" {
			return nil, ErrTextReply
		}
		return nil, nil
	}

	byName := make(map[string]string, len(decls))
	for _, d := range decls {
		byName[d.Name] = d.intentID
	}

	calls := make([]Call, 0, len(msg.ToolCalls))
	for _, tc := range msg.ToolCalls {
		intentID, known := byName[tc.Function.Name]
		if !known {
			// Pass the raw wire name through unconverted. It cannot collide
			// with a real registry id (every real id contains a "."; a wire
			// name never does), so the Tier 1 adapter's own registry lookup
			// will always and correctly treat this as t1_unknown_intent.
			intentID = tc.Function.Name
		}
		args, err := parseArguments(tc.Function.Arguments)
		if err != nil {
			return nil, fmt.Errorf("t1: parse tool call arguments: %w", err)
		}
		calls = append(calls, Call{Intent: intentID, Args: args})
	}
	return calls, nil
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
