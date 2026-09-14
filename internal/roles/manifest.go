// Package roles discovers role folders (extension point 3) and enforces the
// system invariants: unique slugs, exactly one singleton, an orchestrator.
package roles

import (
	"fmt"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Manifest is agents/<slug>/role.yaml.
type Manifest struct {
	Schema        int      `yaml:"schema"`
	Name          string   `yaml:"name"`
	Slug          string   `yaml:"slug"`
	Singleton     bool     `yaml:"singleton"`
	Orchestrator  bool     `yaml:"orchestrator"`
	Description   string   `yaml:"description"`
	SkillsEnabled []string `yaml:"skills_enabled"`
}

var slugRe = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// ParseManifest decodes and validates a role.yaml. dirName is the folder the
// file was found in; slug must match it so discovery and addressing agree.
func ParseManifest(b []byte, dirName string) (Manifest, error) {
	var m Manifest
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return m, fmt.Errorf("role.yaml: %w", err)
	}
	if m.Schema != 1 {
		return m, fmt.Errorf("role.yaml: unsupported schema %d (want 1)", m.Schema)
	}
	if strings.TrimSpace(m.Name) == "" {
		return m, fmt.Errorf("role.yaml: name is required")
	}
	if m.Slug == "" {
		m.Slug = dirName
	}
	if !slugRe.MatchString(m.Slug) {
		return m, fmt.Errorf("role.yaml: slug %q must match %s", m.Slug, slugRe)
	}
	if m.Slug != dirName {
		return m, fmt.Errorf("role.yaml: slug %q does not match folder %q", m.Slug, dirName)
	}
	return m, nil
}
