// Package connectors is the contract every connector implements, and the
// registry the gate resolves functions against. A connector can only be run
// with a gate permit: its arguments and credential arrive inside the permit.
package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"water/internal/gate/permit"
	"water/internal/store"
	"water/internal/twins"
)

type Risk string

const (
	RiskLow    Risk = "low"
	RiskMedium Risk = "medium"
	RiskHigh   Risk = "high"
)

// Function is one tool a connector offers.
//
// Level is the loosest level the function is safe at, which is also what
// it does: R reads, D only drafts, S writes the twin's own state, A has an
// external effect (send, post, invite, write elsewhere), B must never run.
// A manifest may grant that level or tighten it to A or B, never loosen it.
type Function struct {
	Name        string
	Description string
	Schema      Schema
	Level       twins.Level
	Risk        Risk
	// External marks output that carries content written by others, which
	// must be treated as untrusted data downstream.
	External bool
}

// Connector is implemented once per upstream tool.
type Connector interface {
	Name() string
	Functions() []Function
	// Credential names the vault entry this connector needs, or "" "".
	Credential() (service, account string)
	// Invoke must obtain its arguments by redeeming p; it has no other way
	// to learn what to do.
	Invoke(ctx context.Context, p permit.Permit) (json.RawMessage, error)
	// Normalize turns raw output into store records. Raw payloads are never
	// stored.
	Normalize(function string, raw json.RawMessage) ([]store.Record, error)
}

// Registry resolves "connector.function" ids.
type Registry struct {
	conns map[string]Connector
	fns   map[string]Function
}

func NewRegistry(cs ...Connector) (*Registry, error) {
	r := &Registry{conns: map[string]Connector{}, fns: map[string]Function{}}
	for _, c := range cs {
		name := c.Name()
		if name == "" || strings.Contains(name, ".") {
			return nil, fmt.Errorf("connectors: invalid connector name %q", name)
		}
		if _, dup := r.conns[name]; dup {
			return nil, fmt.Errorf("connectors: duplicate connector %q", name)
		}
		r.conns[name] = c
		for _, f := range c.Functions() {
			id := name + "." + f.Name
			if f.Name == "" || strings.Contains(f.Name, ".") {
				return nil, fmt.Errorf("connectors: %s: invalid function name", name)
			}
			if _, dup := r.fns[id]; dup {
				return nil, fmt.Errorf("connectors: duplicate function %s", id)
			}
			if !f.Level.Valid() {
				return nil, fmt.Errorf("connectors: %s has unknown level %q", id, f.Level)
			}
			switch f.Risk {
			case RiskLow, RiskMedium, RiskHigh:
			default:
				return nil, fmt.Errorf("connectors: %s has unknown risk %q", id, f.Risk)
			}
			if err := f.Schema.check(); err != nil {
				return nil, fmt.Errorf("connectors: %s schema: %w", id, err)
			}
			r.fns[id] = f
		}
	}
	return r, nil
}

// Lookup returns the connector and declaration for "connector.function".
func (r *Registry) Lookup(id string) (Connector, Function, bool) {
	f, ok := r.fns[id]
	if !ok {
		return nil, Function{}, false
	}
	name, _, _ := strings.Cut(id, ".")
	return r.conns[name], f, true
}

// Connectors returns every registered connector.
func (r *Registry) Connectors() []Connector {
	out := make([]Connector, 0, len(r.conns))
	for _, c := range r.conns {
		out = append(out, c)
	}
	return out
}
