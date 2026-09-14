package memory

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// MarkdownName is the registry name of the native provider.
const MarkdownName = "markdown"

func init() {
	Register(MarkdownName, func(o Options) (Provider, error) { return NewMarkdown(o) })
}

// Markdown stores one session.md per role under Root/<role>/session.md.
//
// File format (human-editable, git-friendly):
//
//	---
//	schema: 1
//	role: ceo
//	---
//	## [m_1a2b3c] 2026-09-13T11:00:00Z #tag1 #tag2
//	free text, one or more lines
type Markdown struct {
	root   string
	seed   fs.FS
	limits Limits
	mu     sync.Mutex
}

// NewMarkdown constructs the provider. Root is created lazily.
func NewMarkdown(o Options) (*Markdown, error) {
	if o.Root == "" {
		return nil, errors.New("markdown memory: Root is required")
	}
	l := o.Limits
	if l.MaxEntries == 0 && l.MaxBytes == 0 {
		l = DefaultLimits
	}
	return &Markdown{root: o.Root, seed: o.Seed, limits: l}, nil
}

func (m *Markdown) Name() string { return MarkdownName }

// Path returns the on-disk file for a role.
func (m *Markdown) Path(role string) string {
	return filepath.Join(m.root, filepath.Base(role), "session.md")
}

var headerRe = regexp.MustCompile(`^## \[([A-Za-z0-9_\-]+)\]\s+(\S+)(.*)$`)

func (m *Markdown) ensure(role string) error {
	p := m.Path(role)
	if _, err := os.Stat(p); err == nil {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	var content []byte
	if m.seed != nil {
		if b, err := fs.ReadFile(m.seed, path.Join(role, "memory", "session.md")); err == nil {
			content = b
		}
	}
	if content == nil {
		content = []byte(fmt.Sprintf("---\nschema: 1\nrole: %s\nkind: memory\n---\n", role))
	}
	return os.WriteFile(p, content, 0o644)
}

type parsed struct {
	preamble string // frontmatter + comments before first entry
	entries  []Entry
}

func (m *Markdown) read(role string) (*parsed, error) {
	if err := m.ensure(role); err != nil {
		return nil, err
	}
	f, err := os.Open(m.Path(role))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	p := &parsed{}
	var pre strings.Builder
	var cur *Entry
	var body strings.Builder
	flush := func() {
		if cur != nil {
			cur.Text = strings.TrimSpace(body.String())
			p.entries = append(p.entries, *cur)
			cur = nil
			body.Reset()
		}
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if mm := headerRe.FindStringSubmatch(line); mm != nil {
			flush()
			e := Entry{ID: mm[1]}
			if t, err := time.Parse(time.RFC3339, mm[2]); err == nil {
				e.CreatedAt = t
			}
			for _, tok := range strings.Fields(mm[3]) {
				if strings.HasPrefix(tok, "#") && len(tok) > 1 {
					e.Tags = append(e.Tags, tok[1:])
				}
			}
			cur = &e
			continue
		}
		if cur != nil {
			body.WriteString(line + "\n")
		} else {
			pre.WriteString(line + "\n")
		}
	}
	flush()
	p.preamble = pre.String()
	return p, sc.Err()
}

func (m *Markdown) write(role string, p *parsed) error {
	if err := CheckBounds(p.entries, m.limits); err != nil {
		return err
	}
	var sb strings.Builder
	sb.WriteString(strings.TrimRight(p.preamble, "\n") + "\n")
	for _, e := range p.entries {
		sb.WriteString("\n## [" + e.ID + "] " + e.CreatedAt.UTC().Format(time.RFC3339))
		for _, t := range e.Tags {
			sb.WriteString(" #" + t)
		}
		sb.WriteString("\n" + strings.TrimSpace(e.Text) + "\n")
	}
	tmp := m.Path(role) + ".tmp"
	if err := os.WriteFile(tmp, []byte(sb.String()), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.Path(role))
}

// Snapshot returns the frozen entry list. Bounds are checked on read too so a
// hand-edited file that grew past limits fails loudly rather than loading.
func (m *Markdown) Snapshot(_ context.Context, role string) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.read(role)
	if err != nil {
		return nil, err
	}
	if err := CheckBounds(p.entries, m.limits); err != nil {
		return p.entries, err
	}
	return p.entries, nil
}

func (m *Markdown) Add(_ context.Context, role string, e Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := lockRole(m.root, role)
	if err != nil {
		return err
	}
	defer unlock()
	p, err := m.read(role)
	if err != nil {
		return err
	}
	if e.ID == "" {
		e.ID = NewID()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	for _, x := range p.entries {
		if x.ID == e.ID {
			return fmt.Errorf("memory entry %s already exists", e.ID)
		}
	}
	p.entries = append(p.entries, e)
	return m.write(role, p)
}

func (m *Markdown) Replace(_ context.Context, role, id string, e Entry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := lockRole(m.root, role)
	if err != nil {
		return err
	}
	defer unlock()
	p, err := m.read(role)
	if err != nil {
		return err
	}
	for i, x := range p.entries {
		if x.ID == id {
			e.ID = id
			if e.CreatedAt.IsZero() {
				e.CreatedAt = x.CreatedAt
			}
			p.entries[i] = e
			return m.write(role, p)
		}
	}
	return fmt.Errorf("%w: %s", ErrNotFound, id)
}

func (m *Markdown) Remove(_ context.Context, role, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := lockRole(m.root, role)
	if err != nil {
		return err
	}
	defer unlock()
	p, err := m.read(role)
	if err != nil {
		return err
	}
	for i, x := range p.entries {
		if x.ID == id {
			p.entries = append(p.entries[:i], p.entries[i+1:]...)
			return m.write(role, p)
		}
	}
	return fmt.Errorf("%w: %s", ErrNotFound, id)
}

// Prune removes the oldest entries until the set fits within limits. It is
// the explicit action ErrBoundsExceeded asks the user to take. Returns the
// removed entries.
func (m *Markdown) Prune(_ context.Context, role string) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := lockRole(m.root, role)
	if err != nil {
		return nil, err
	}
	defer unlock()
	p, err := m.read(role)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(p.entries, func(i, j int) bool { return p.entries[i].CreatedAt.Before(p.entries[j].CreatedAt) })
	var removed []Entry
	for CheckBounds(p.entries, m.limits) != nil && len(p.entries) > 0 {
		removed = append(removed, p.entries[0])
		p.entries = p.entries[1:]
	}
	return removed, m.write(role, p)
}

// Size returns entry count and approximate bytes for status output.
func (m *Markdown) Size(role string) (entries, bytes int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.read(role)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range p.entries {
		bytes += len(e.Text) + len(e.ID) + 40
	}
	return len(p.entries), bytes, nil
}

// Pruner is implemented by providers that support explicit pruning.
type Pruner interface {
	Prune(ctx context.Context, role string) ([]Entry, error)
}

// Sizer is implemented by providers that can report usage cheaply.
type Sizer interface {
	Size(role string) (entries, bytes int, err error)
}

// NewID returns a short random entry id.
func NewID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return "m_" + hex.EncodeToString(b)
}
