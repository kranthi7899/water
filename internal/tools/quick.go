package tools

import (
	"fmt"
	"strings"
)

// QuickFunction is one of the sous chef's read-only reflex handlers,
// exposed as an MCP tool the model can call directly for a fast, cheap
// local lookup — never proxied through the daemon's gate, unlike
// TwinFunction. ID is a reflex function id ("quick.calendar"); Tool is its
// MCP-safe name ("quick__calendar").
type QuickFunction struct {
	ID          string `json:"id"`
	Tool        string `json:"tool"`
	Description string `json:"description"`
	// Schema is a pre-rendered JSON Schema object, kept opaque here (the
	// same convention as TwinFunction.Schema) so this package need not
	// import internal/nervous/intents or internal/nervous/slots.
	Schema []byte `json:"schema"`
}

// QuickToolName derives an MCP-safe tool name from a quick function id, the
// same way TwinToolName does for a connector.function id.
func QuickToolName(id string) string { return strings.ReplaceAll(id, ".", "__") }

func (p *Policy) quickByTool(tool string) (QuickFunction, bool) {
	for _, f := range p.Quick {
		if f.Tool == tool {
			return f, true
		}
	}
	return QuickFunction{}, false
}

// Validate checks a policy's Twin and Quick lists are consistent before it
// is written to disk or handed to a model: no two tools share a name across
// either list, no Twin tool is named like a quick tool, and no Quick entry
// is missing the "quick." id prefix (which would make it indistinguishable
// from a manifest connector function to anything that just checks the id
// shape). This is what keeps handleQuickInvoke and handleToolInvoke's split
// unambiguous at the policy level, not just at the two daemon endpoints.
func (p *Policy) Validate() error {
	seen := make(map[string]string, len(p.Twin)+len(p.Quick))
	for _, f := range p.Twin {
		if strings.HasPrefix(f.Tool, "quick__") {
			return fmt.Errorf("tool policy: twin function %q is named like a quick tool", f.ID)
		}
		if prev, dup := seen[f.Tool]; dup {
			return fmt.Errorf("tool policy: tool name %q used by both %q and %q", f.Tool, prev, f.ID)
		}
		seen[f.Tool] = f.ID
	}
	for _, f := range p.Quick {
		if !strings.HasPrefix(f.ID, "quick.") {
			return fmt.Errorf("tool policy: quick function %q must have a \"quick.\" id prefix", f.ID)
		}
		if prev, dup := seen[f.Tool]; dup {
			return fmt.Errorf("tool policy: tool name %q used by both %q and %q", f.Tool, prev, f.ID)
		}
		seen[f.Tool] = f.ID
	}
	return nil
}
