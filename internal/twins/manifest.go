// Package twins loads a twin's manifest: the only source of which connector
// functions a twin may use and at what access level. It is deliberately
// minimal; Slice E generalizes it.
package twins

import (
	"bytes"
	"fmt"
	"io/fs"
	"path"
	"time"

	"gopkg.in/yaml.v3"
)

// Level is an access level. The gate enforces its meaning.
type Level string

const (
	R Level = "R" // read
	D Level = "D" // draft only; nothing leaves
	A Level = "A" // execute only with an approved envelope for the exact payload
	S Level = "S" // autonomous, writes to the twin's own state only
	B Level = "B" // blocked
)

func (l Level) Valid() bool {
	switch l {
	case R, D, A, S, B:
		return true
	}
	return false
}

// Duration is a time.Duration written as "1h", "5h", "30m".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return err
	}
	*d = Duration(v)
	return nil
}

type RateCap struct {
	Max int      `yaml:"max"`
	Per Duration `yaml:"per"`
}

type Function struct {
	Name  string   `yaml:"name"`
	Level Level    `yaml:"level"`
	Rate  *RateCap `yaml:"rate"`
}

type Connector struct {
	Name      string     `yaml:"name"`
	Functions []Function `yaml:"functions"`
}

// Usage caps model calls per window. AutoModelCalls is the P2 share.
type Usage struct {
	Window         Duration `yaml:"window"`
	ModelCalls     int      `yaml:"model_calls"`
	AutoModelCalls int      `yaml:"auto_model_calls"`
}

// Models names which model string backs each tier. Fast serves the
// conversation plane; Strong serves packet and playbook work. An empty string
// means the CLI's own default model.
type Models struct {
	Fast   string `yaml:"fast"`
	Strong string `yaml:"strong"`
}

// Tier names a model tier.
type Tier string

const (
	TierFast   Tier = "fast"
	TierStrong Tier = "strong"
)

// defaultFastModel is used when a manifest omits models.fast.
const defaultFastModel = "haiku"

type Manifest struct {
	ID            string      `yaml:"id"`
	Name          string      `yaml:"name"`
	Usage         Usage       `yaml:"usage"`
	Models        Models      `yaml:"models"`
	Connectors    []Connector `yaml:"connectors"`
	AutoAllowlist []string    `yaml:"auto_allowlist"`

	functions map[string]Function
	auto      map[string]bool
}

// ModelFor resolves the model string to request for a tier. "" (the CLI's
// default) is a valid, deliberate choice for the strong tier.
func (m *Manifest) ModelFor(t Tier) string {
	switch t {
	case TierStrong:
		return m.Models.Strong
	default:
		return m.Models.Fast
	}
}

// Load reads twins/<id>/twin.yaml from fsys (the embedded repo root).
func Load(fsys fs.FS, id string) (*Manifest, error) {
	b, err := fs.ReadFile(fsys, path.Join("twins", id, "twin.yaml"))
	if err != nil {
		return nil, err
	}
	m, err := Parse(b)
	if err != nil {
		return nil, fmt.Errorf("twin %s: %w", id, err)
	}
	if m.ID != id {
		return nil, fmt.Errorf("twin %s: manifest id %q does not match folder", id, m.ID)
	}
	return m, nil
}

// Parse decodes and strictly validates a manifest. Unknown keys, unknown
// levels and duplicates are errors: a typo must never widen access.
func Parse(b []byte) (*Manifest, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("manifest: %w", err)
	}
	if m.ID == "" || m.Name == "" {
		return nil, fmt.Errorf("manifest: id and name are required")
	}
	if m.Usage.Window <= 0 || m.Usage.ModelCalls <= 0 || m.Usage.AutoModelCalls < 0 || m.Usage.AutoModelCalls > m.Usage.ModelCalls {
		return nil, fmt.Errorf("manifest: usage needs a positive window and model_calls, and auto_model_calls within model_calls")
	}
	if m.Models.Fast == "" {
		m.Models.Fast = defaultFastModel
	}
	m.functions = map[string]Function{}
	seenConn := map[string]bool{}
	for _, c := range m.Connectors {
		if c.Name == "" {
			return nil, fmt.Errorf("manifest: connector without a name")
		}
		if seenConn[c.Name] {
			return nil, fmt.Errorf("manifest: duplicate connector %q", c.Name)
		}
		seenConn[c.Name] = true
		for _, f := range c.Functions {
			id := c.Name + "." + f.Name
			if f.Name == "" {
				return nil, fmt.Errorf("manifest: connector %s has a function without a name", c.Name)
			}
			if !f.Level.Valid() {
				return nil, fmt.Errorf("manifest: %s has unknown level %q", id, f.Level)
			}
			if _, dup := m.functions[id]; dup {
				return nil, fmt.Errorf("manifest: duplicate function %s", id)
			}
			if f.Rate != nil && (f.Rate.Max <= 0 || f.Rate.Per <= 0) {
				return nil, fmt.Errorf("manifest: %s rate needs positive max and per", id)
			}
			m.functions[id] = f
		}
	}
	m.auto = map[string]bool{}
	for _, id := range m.AutoAllowlist {
		f, ok := m.functions[id]
		if !ok {
			return nil, fmt.Errorf("manifest: auto_allowlist names unlisted function %s", id)
		}
		if f.Level != R && f.Level != D {
			return nil, fmt.Errorf("manifest: auto_allowlist may hold only R or D functions, %s is %s", id, f.Level)
		}
		if m.auto[id] {
			return nil, fmt.Errorf("manifest: duplicate auto_allowlist entry %s", id)
		}
		m.auto[id] = true
	}
	return &m, nil
}

// Function returns the declaration for "connector.function".
func (m *Manifest) Function(id string) (Function, bool) {
	f, ok := m.functions[id]
	return f, ok
}

// AutoAllowed reports whether P2 background work may call id.
func (m *Manifest) AutoAllowed(id string) bool { return m.auto[id] }

// FunctionIDs lists every declared "connector.function".
func (m *Manifest) FunctionIDs() []string {
	var out []string
	for _, c := range m.Connectors {
		for _, f := range c.Functions {
			out = append(out, c.Name+"."+f.Name)
		}
	}
	return out
}
