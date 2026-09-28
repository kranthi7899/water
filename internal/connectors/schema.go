package connectors

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
)

// Schema is the subset of JSON Schema connectors use: a flat object whose
// properties are scalars or arrays of scalars. Unknown properties are
// rejected, so arguments cannot smuggle fields past review.
type Schema struct {
	Properties map[string]Property `json:"properties"`
	Required   []string            `json:"required,omitempty"`
}

type Property struct {
	Type        string    `json:"type"` // string | integer | number | boolean | array
	Items       *Property `json:"items,omitempty"`
	Description string    `json:"description,omitempty"`
}

// MarshalJSON renders the schema as a JSON Schema document.
func (s Schema) MarshalJSON() ([]byte, error) {
	props := s.Properties
	if props == nil {
		props = map[string]Property{}
	}
	return json.Marshal(struct {
		Type       string              `json:"type"`
		Properties map[string]Property `json:"properties"`
		Required   []string            `json:"required,omitempty"`
		Additional bool                `json:"additionalProperties"`
	}{"object", props, s.Required, false})
}

func (s Schema) check() error {
	for _, r := range s.Required {
		if _, ok := s.Properties[r]; !ok {
			return fmt.Errorf("required %q is not a property", r)
		}
	}
	for name, p := range s.Properties {
		switch p.Type {
		case "string", "integer", "number", "boolean":
		case "array":
			if p.Items == nil || p.Items.Type == "array" || p.Items.Items != nil {
				return fmt.Errorf("%s: arrays hold scalars", name)
			}
		default:
			return fmt.Errorf("%s: unsupported type %q", name, p.Type)
		}
	}
	return nil
}

// Validate checks args against the schema.
func (s Schema) Validate(args map[string]any) error {
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p, ok := s.Properties[k]
		if !ok {
			return fmt.Errorf("unexpected argument %q", k)
		}
		if err := p.validate(k, args[k]); err != nil {
			return err
		}
	}
	for _, r := range s.Required {
		if _, ok := args[r]; !ok {
			return fmt.Errorf("missing argument %q", r)
		}
	}
	return nil
}

func (p Property) validate(name string, v any) error {
	bad := fmt.Errorf("argument %q must be %s", name, p.Type)
	switch p.Type {
	case "string":
		if _, ok := v.(string); !ok {
			return bad
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			return bad
		}
	case "number", "integer":
		f, ok := number(v)
		if !ok || (p.Type == "integer" && f != math.Trunc(f)) {
			return bad
		}
	case "array":
		items, ok := v.([]any)
		if !ok {
			if ss, isStrings := v.([]string); isStrings {
				for _, s := range ss {
					items = append(items, s)
				}
			} else {
				return bad
			}
		}
		for _, it := range items {
			if err := p.Items.validate(name+"[]", it); err != nil {
				return err
			}
		}
	}
	return nil
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}
