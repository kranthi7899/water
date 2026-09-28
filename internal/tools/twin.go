package tools

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"
)

// TwinFunction is one of the twin manifest's connector functions, exposed as
// an MCP tool. The model calls Tool; the MCP server proxies it to the
// daemon's gate as ID ("connector.function"), over TwinSocket with
// TwinToken. Everything the gate actually enforces (level, taint, approval,
// rate caps, audit) lives in the daemon; the policy only carries enough
// metadata to list and route the call.
type TwinFunction struct {
	ID          string `json:"id"`
	Tool        string `json:"tool"`
	Description string `json:"description"`
	// Schema is a pre-rendered JSON Schema object (connectors.Schema's own
	// MarshalJSON), kept opaque here so this package need not import
	// connectors.
	Schema []byte `json:"schema"`
}

// TwinToolName derives an MCP-safe tool name from a "connector.function" id.
func TwinToolName(id string) string { return strings.ReplaceAll(id, ".", "__") }

func (p *Policy) twinByTool(tool string) (TwinFunction, bool) {
	for _, f := range p.Twin {
		if f.Tool == tool {
			return f, true
		}
	}
	return TwinFunction{}, false
}

// twinHTTPClient dials the daemon's Unix socket for one call.
func twinHTTPClient(socketPath string) *http.Client {
	return &http.Client{
		Timeout: 55 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		},
	}
}
