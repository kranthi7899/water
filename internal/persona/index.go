package persona

import (
	"encoding/json"
	"errors"
	"io/fs"
	"path"
	"strings"
)

// Index is the hidden traceability map for experience.md: each sentence of
// the uncited prose points at the verified source record it was distilled
// from. Stored as agents/<slug>/.index.json, never rendered inline.
type Index struct {
	Schema  int          `json:"schema"`
	Role    string       `json:"role"`
	Entries []IndexEntry `json:"entries"`
}

// IndexEntry maps one sentence (or claim) to its source record.
type IndexEntry struct {
	Sentence   string  `json:"sentence"`
	SourceID   string  `json:"source_id"`
	Source     string  `json:"source"`               // human-readable locator (file, URL, record key)
	Confidence float64 `json:"confidence,omitempty"` // 0..1, optional
}

// LoadIndex reads <roleDir>/.index.json. A missing file yields an empty index.
func LoadIndex(fsys fs.FS, roleDir string) (*Index, error) {
	b, err := fs.ReadFile(fsys, path.Join(roleDir, ".index.json"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &Index{Schema: 1}, nil
		}
		return nil, err
	}
	var idx Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return nil, err
	}
	return &idx, nil
}

// Lookup traces a visible claim back to source records. Matching is
// normalised substring in both directions so a partial quote still resolves.
func (i *Index) Lookup(claim string) []IndexEntry {
	c := normalise(claim)
	if c == "" {
		return nil
	}
	var out []IndexEntry
	for _, e := range i.Entries {
		s := normalise(e.Sentence)
		if s == "" {
			continue
		}
		if strings.Contains(s, c) || strings.Contains(c, s) {
			out = append(out, e)
		}
	}
	return out
}

func normalise(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.Join(strings.Fields(s), " ")
}
