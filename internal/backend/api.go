package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// API is the metered fallback: direct HTTPS to the Anthropic Messages API with
// a key from config or ANTHROPIC_API_KEY. Every Response it returns is Metered.
// The key is scoped to this process's HTTP client and is never placed in a
// subprocess environment.
type API struct {
	Key     string // explicit key from config; falls back to ANTHROPIC_API_KEY
	Model   string
	BaseURL string
	Client  *http.Client
}

const APIName = "api"

func init() { Default.Register(&API{}) }

func (a *API) Name() string { return APIName }

func (a *API) key() string {
	if a.Key != "" {
		return a.Key
	}
	return os.Getenv("ANTHROPIC_API_KEY")
}

func (a *API) model() string {
	if a.Model != "" {
		return a.Model
	}
	return "claude-opus-5"
}

func (a *API) base() string {
	if a.BaseURL != "" {
		return strings.TrimRight(a.BaseURL, "/")
	}
	if v := os.Getenv("ANTHROPIC_BASE_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://api.anthropic.com"
}

func (a *API) Available(context.Context) Availability {
	av := Availability{Installed: true, Metered: true}
	if a.key() == "" {
		av.Detail = "metered; no API key configured (api.key or ANTHROPIC_API_KEY)"
		return av
	}
	av.Authed = true
	av.Detail = "metered; API key present — bills per token"
	return av
}

type apiRequest struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	System    string       `json:"system,omitempty"`
	Messages  []apiMessage `json:"messages"`
}

type apiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type apiResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Usage      struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

func (a *API) Run(ctx context.Context, req Request) (Response, error) {
	key := a.key()
	if key == "" {
		return Response{}, fmt.Errorf("api backend: no API key configured")
	}
	body, _ := json.Marshal(apiRequest{
		Model:     a.model(),
		MaxTokens: 16000,
		System:    req.System,
		Messages:  []apiMessage{{Role: "user", Content: req.Prompt}},
	})
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	hreq, err := http.NewRequestWithContext(cctx, http.MethodPost, a.base()+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	hreq.Header.Set("content-type", "application/json")
	hreq.Header.Set("x-api-key", key)
	hreq.Header.Set("anthropic-version", "2023-06-01")

	client := a.Client
	if client == nil {
		client = &http.Client{}
	}
	start := time.Now()
	hres, err := client.Do(hreq)
	if err != nil {
		return Response{}, fmt.Errorf("api backend: %w", err)
	}
	defer hres.Body.Close()
	raw, _ := io.ReadAll(hres.Body)
	resp := Response{Raw: string(raw), Metered: true, Backend: a.Name(), Duration: time.Since(start)}

	var ar apiResponse
	if jerr := json.Unmarshal(raw, &ar); jerr != nil {
		return resp, fmt.Errorf("api backend: HTTP %d: %s", hres.StatusCode, firstLine(string(raw)))
	}
	if ar.Error != nil {
		return resp, fmt.Errorf("api backend: %s: %s", ar.Error.Type, ar.Error.Message)
	}
	if hres.StatusCode >= 300 {
		return resp, fmt.Errorf("api backend: HTTP %d", hres.StatusCode)
	}
	resp.InputTokens = ar.Usage.InputTokens
	resp.OutputTokens = ar.Usage.OutputTokens
	if ar.StopReason == "refusal" {
		return resp, fmt.Errorf("api backend: model refused the request")
	}
	var sb strings.Builder
	for _, c := range ar.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	resp.Text = strings.TrimSpace(sb.String())
	return resp, nil
}
