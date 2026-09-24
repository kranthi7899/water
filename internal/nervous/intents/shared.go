package intents

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Shared is twins/<id>/intents/_shared.yaml: the template rules every
// intent file may reference, the words normalization drops, the words that
// always block a Tier 0/1 match (defense in depth over the eligibility
// check in a later task), the phrases that mark a turn as needing
// reasoning, and the phrases that mark a possible-miss correction.
type Shared struct {
	Rules         map[string]string `yaml:"rules"`
	SkipWords     []string          `yaml:"skip_words"`
	DenyWords     []string          `yaml:"deny_words"`
	EscalateWords []string          `yaml:"escalate_words"`
	ClauseJoiners []string          `yaml:"clause_joiners"`
	Corrections   []string          `yaml:"corrections"`
}

func (s Shared) skipSet() map[string]bool {
	m := make(map[string]bool, len(s.SkipWords))
	for _, w := range s.SkipWords {
		m[w] = true
	}
	return m
}

// parseShared strictly decodes _shared.yaml. Unknown keys are an error, the
// same posture as every other plain-file loader in this repo.
func parseShared(b []byte) (Shared, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var s Shared
	if err := dec.Decode(&s); err != nil {
		return Shared{}, fmt.Errorf("yaml: %w", err)
	}
	return s, nil
}
