// Package session stores per-role, append-only JSONL transcripts (Part 4.3).
//
// Files live under <root>/<role>/<slug>.jsonl — the role's own directory, so
// a transcript is as isolated as memory is. Nothing here ever reads another
// role's directory.
//
// Compaction never deadlocks: summary checkpoints are appended incrementally
// as the transcript grows, and Open reads only the tail after the last
// checkpoint, so a session too large to load whole is still resumable and
// still compactable.
//
// Promotion to memory stays manual (/remember). A transcript never becomes a
// memory entry by itself; candidate signals are logged for a future opt-in
// suggester and nothing more.
package session

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Entry kinds.
const (
	KindHeader    = "header"
	KindUser      = "user"
	KindAssistant = "assistant"
	KindSystem    = "system"
	KindSummary   = "summary" // compaction checkpoint: covers everything before it
	KindFlag      = "flag"
	KindSignal    = "signal" // candidate memory signal (never auto-promoted)
	KindAttach    = "attach"
	KindConsult   = "consult"
	KindPin       = "pin"
)

// Entry is one JSONL line.
type Entry struct {
	Kind      string            `json:"kind"`
	At        time.Time         `json:"at"`
	Turn      int               `json:"turn,omitempty"`
	Text      string            `json:"text,omitempty"`
	Role      string            `json:"role,omitempty"` // header: owning role; consult: the consulted role
	Slug      string            `json:"slug,omitempty"`
	Name      string            `json:"name,omitempty"` // header/pin: display name
	Pinned    bool              `json:"pinned,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
	Backend   string            `json:"backend,omitempty"`
	Skills    []string          `json:"skills,omitempty"`
	MemoryIDs []string          `json:"memory_ids,omitempty"`
	InboxIDs  []string          `json:"inbox_ids,omitempty"`
}

// Store is the per-role transcript store.
type Store struct {
	Root string
	Role string
	mu   sync.Mutex
}

// New opens a store for role under root.
func New(root, role string) *Store { return &Store{Root: root, Role: role} }

// Dir is <root>/<role>.
func (s *Store) Dir() string { return filepath.Join(s.Root, filepath.Base(s.Role)) }

// Path is the transcript file for slug.
func (s *Store) Path(slug string) string {
	return filepath.Join(s.Dir(), filepath.Base(slug)+".jsonl")
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slugify derives a stable, filename-safe slug from text (first words) plus a
// date suffix, uniquified against existing files.
func (s *Store) Slugify(text string, now time.Time) string {
	words := strings.Fields(strings.ToLower(text))
	if len(words) > 5 {
		words = words[:5]
	}
	base := strings.Trim(slugRe.ReplaceAllString(strings.Join(words, "-"), "-"), "-")
	if len(base) > 40 {
		base = base[:40]
	}
	if base == "" {
		base = "session"
	}
	base = base + "-" + now.Format("0102")
	slug := base
	for i := 2; ; i++ {
		if _, err := os.Stat(s.Path(slug)); errors.Is(err, os.ErrNotExist) {
			return slug
		}
		slug = fmt.Sprintf("%s-%d", base, i)
	}
}

// Create starts a new transcript and writes its header.
func (s *Store) Create(slug, name string) error {
	if err := os.MkdirAll(s.Dir(), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(s.Path(slug)); err == nil {
		return fmt.Errorf("session %q already exists", slug)
	}
	return s.Append(slug, Entry{Kind: KindHeader, Role: s.Role, Slug: slug, Name: name})
}

// Append adds one entry (At defaults to now). Append-only; never rewrites.
func (s *Store) Append(slug string, e Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	if err := os.MkdirAll(s.Dir(), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(s.Path(slug), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

// Exists reports whether slug has a transcript.
func (s *Store) Exists(slug string) bool {
	_, err := os.Stat(s.Path(slug))
	return err == nil
}

// TailBytes bounds how much of a transcript Open reads from the end when
// looking for the last summary checkpoint. It is the guarantee behind
// "compaction never requires loading the whole file".
const TailBytes = 512 * 1024

// Context is the active conversational context: the last summary (if any)
// plus every entry after it. It is what a turn's prompt is built from.
type Context struct {
	Slug     string
	Header   Entry
	Summary  *Entry
	Entries  []Entry // after the summary
	Turns    int     // highest turn number seen
	Pinned   bool
	Name     string
	ReadFrom int64 // byte offset the tail read started at (diagnostics)
}

// Open loads the active context by reading only the tail of the file: it
// seeks back TailBytes (or to the start), scans forward, and keeps the last
// summary checkpoint and everything after it. Entries before that are never
// loaded into memory.
func (s *Store) Open(slug string) (*Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := os.Open(s.Path(slug))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	c := &Context{Slug: slug}
	// Header lives on the first line; read it separately and cheaply.
	hr := bufio.NewReader(f)
	if line, err := hr.ReadBytes('\n'); err == nil || len(line) > 0 {
		var h Entry
		if json.Unmarshal(line, &h) == nil && h.Kind == KindHeader {
			c.Header, c.Name = h, h.Name
		}
	}
	start := st.Size() - TailBytes
	if start < 0 {
		start = 0
	}
	c.ReadFrom = start
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	first := true
	for sc.Scan() {
		line := sc.Bytes()
		if first {
			first = false
			if start > 0 {
				// We may have landed mid-line; drop the partial record.
				continue
			}
		}
		var e Entry
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		switch e.Kind {
		case KindHeader:
			c.Header, c.Name = e, e.Name
			c.Pinned = c.Pinned || e.Pinned
			continue
		case KindPin:
			c.Pinned = e.Pinned
			if e.Name != "" {
				c.Name = e.Name
			}
			continue
		case KindSummary:
			c.Summary = &e
			c.Entries = c.Entries[:0]
			continue
		}
		if e.Turn > c.Turns {
			c.Turns = e.Turn
		}
		c.Entries = append(c.Entries, e)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if !c.Pinned {
		c.Pinned = s.pinned(slug)
	}
	return c, nil
}

// pinned scans only pin/header lines cheaply for older files where the pin
// may sit before the tail window.
func (s *Store) pinned(slug string) bool {
	f, err := os.Open(s.Path(slug))
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	p := false
	for sc.Scan() {
		line := sc.Bytes()
		if !strings.Contains(string(line), `"kind":"pin"`) && !strings.Contains(string(line), `"kind":"header"`) {
			continue
		}
		var e Entry
		if json.Unmarshal(line, &e) == nil && (e.Kind == KindPin || e.Kind == KindHeader) {
			p = e.Pinned
		}
	}
	return p
}

// Summariser produces a compaction summary from the active context. The
// model call happens in the caller's backend; this package only stores it.
type Summariser func(c *Context, focus string) (string, error)

// Compact appends a summary checkpoint for the current active context. It
// never loads more than Open does, so an oversized session compacts fine.
func (s *Store) Compact(slug, focus string, sum Summariser) (*Context, error) {
	c, err := s.Open(slug)
	if err != nil {
		return nil, err
	}
	text, err := sum(c, focus)
	if err != nil {
		return nil, err
	}
	if err := s.Append(slug, Entry{Kind: KindSummary, Text: text, Turn: c.Turns, Meta: map[string]string{"focus": focus}}); err != nil {
		return nil, err
	}
	return s.Open(slug)
}

// AutoCompactEvery is the turn interval at which a checkpoint is appended
// automatically so the tail window never has to hold an unbounded history.
const AutoCompactEvery = 40

// Info summarises one transcript for listings.
type Info struct {
	Slug     string    `json:"slug"`
	Name     string    `json:"name"`
	Role     string    `json:"role"`
	Pinned   bool      `json:"pinned"`
	Modified time.Time `json:"modified"`
	Bytes    int64     `json:"bytes"`
	Turns    int       `json:"turns"`
}

// List returns this role's transcripts, newest first.
func (s *Store) List() ([]Info, error) {
	entries, err := os.ReadDir(s.Dir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		slug := strings.TrimSuffix(e.Name(), ".jsonl")
		c, err := s.Open(slug)
		if err != nil {
			continue
		}
		out = append(out, Info{Slug: slug, Name: c.Name, Role: s.Role, Pinned: c.Pinned, Modified: fi.ModTime(), Bytes: fi.Size(), Turns: c.Turns})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modified.After(out[j].Modified) })
	return out, nil
}

// Pin marks (or unmarks) a session pinned with an optional display name.
// Pinned sessions are exempt from pruning.
func (s *Store) Pin(slug, name string, pinned bool) error {
	if !s.Exists(slug) {
		return fmt.Errorf("session %q not found", slug)
	}
	return s.Append(slug, Entry{Kind: KindPin, Pinned: pinned, Name: name})
}

// Delete removes a transcript. Confirmation is the caller's responsibility.
func (s *Store) Delete(slug string) error {
	if !s.Exists(slug) {
		return fmt.Errorf("session %q not found", slug)
	}
	return os.Remove(s.Path(slug))
}

// Retention is count-based with an age backstop (Part 4.3). Pinned sessions
// are exempt. Keep <= 0 disables count pruning; MaxAge <= 0 disables age.
type Retention struct {
	Keep   int
	MaxAge time.Duration
}

// Prune deletes unpinned transcripts beyond Keep (newest kept) or older than
// MaxAge, and returns the removed slugs.
func (s *Store) Prune(r Retention, now time.Time) ([]string, error) {
	infos, err := s.List()
	if err != nil {
		return nil, err
	}
	var removed []string
	kept := 0
	for _, in := range infos {
		if in.Pinned {
			continue
		}
		tooOld := r.MaxAge > 0 && now.Sub(in.Modified) > r.MaxAge
		tooMany := r.Keep > 0 && kept >= r.Keep
		if tooOld || tooMany {
			if err := os.Remove(s.Path(in.Slug)); err == nil {
				removed = append(removed, in.Slug)
			}
			continue
		}
		kept++
	}
	return removed, nil
}
