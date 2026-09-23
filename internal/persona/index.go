package persona

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
)

// Index is the hidden traceability map for experience.md: each sentence of
// the uncited prose points at the verified source record it was distilled
// from. Stored as agents/<slug>/.index.json. Sources are never rendered
// inline; only a per-lesson corroboration count reaches the prompt.
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

// Corroboration counts the distinct source records whose indexed sentence is
// word-for-word the given lesson (after whitespace/case normalisation).
func (i *Index) Corroboration(lesson string) int {
	if i == nil {
		return 0
	}
	l := normalise(lesson)
	seen := map[string]bool{}
	for _, e := range i.Entries {
		if l != "" && normalise(e.Sentence) == l {
			seen[e.SourceID] = true
		}
	}
	return len(seen)
}

// annotate appends "(N independent cases)" to each experience paragraph the
// index backs, so souls can weight lessons by corroboration. Only the count
// is surfaced — never a source name — and unindexed paragraphs (the preamble)
// are left as written.
func (i *Index) annotate(prose string) string {
	paras := strings.Split(prose, "\n\n")
	for k, p := range paras {
		switch n := i.Corroboration(p); {
		case n == 1:
			paras[k] = strings.TrimRight(p, " \n") + " (1 independent case)"
		case n > 1:
			paras[k] = fmt.Sprintf("%s (%d independent cases)", strings.TrimRight(p, " \n"), n)
		}
	}
	return strings.Join(paras, "\n\n")
}

func normalise(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.Join(strings.Fields(s), " ")
}
