// Package persona loads role persona content (soul.md, experience.md, skills)
// through a Source seam so content can be embedded today and user-editable
// later without a rewrite.
package persona

import (
	"errors"
	"io/fs"
	"os"
	"sort"
)

// Source provides the agents tree as an fs.FS rooted at the agents directory
// (i.e. Open("ceo/role.yaml")). Implementations: EmbeddedSource, DirSource,
// and Overlay which shadows one with another per-file.
type Source interface {
	Name() string
	FS() fs.FS
}

// EmbeddedSource serves content compiled into the binary.
type EmbeddedSource struct{ fsys fs.FS }

// NewEmbedded wraps an fs.FS rooted at the agents directory.
func NewEmbedded(fsys fs.FS) *EmbeddedSource { return &EmbeddedSource{fsys: fsys} }
func (e *EmbeddedSource) Name() string       { return "embedded" }
func (e *EmbeddedSource) FS() fs.FS          { return e.fsys }

// DirSource serves content from a directory on disk (--agents-dir).
type DirSource struct{ Dir string }

func NewDir(dir string) *DirSource { return &DirSource{Dir: dir} }
func (d *DirSource) Name() string  { return "dir:" + d.Dir }
func (d *DirSource) FS() fs.FS     { return os.DirFS(d.Dir) }

// Overlay resolves per-file: Upper shadows Lower. Directory listings are the
// union of both. This is what makes "hidden in v1, customizable in v2" a config
// flip.
type Overlay struct {
	Upper Source
	Lower Source
}

func NewOverlay(upper, lower Source) *Overlay { return &Overlay{Upper: upper, Lower: lower} }
func (o *Overlay) Name() string               { return o.Upper.Name() + " over " + o.Lower.Name() }
func (o *Overlay) FS() fs.FS                  { return overlayFS{upper: o.Upper.FS(), lower: o.Lower.FS()} }

type overlayFS struct{ upper, lower fs.FS }

func (o overlayFS) Open(name string) (fs.File, error) {
	if f, err := o.upper.Open(name); err == nil {
		return f, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return o.lower.Open(name)
}

func (o overlayFS) ReadDir(name string) ([]fs.DirEntry, error) {
	seen := map[string]fs.DirEntry{}
	up, uerr := fs.ReadDir(o.upper, name)
	lo, lerr := fs.ReadDir(o.lower, name)
	if uerr != nil && lerr != nil {
		return nil, uerr
	}
	for _, e := range lo {
		seen[e.Name()] = e
	}
	for _, e := range up {
		seen[e.Name()] = e
	}
	out := make([]fs.DirEntry, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

func (o overlayFS) ReadFile(name string) ([]byte, error) {
	if b, err := fs.ReadFile(o.upper, name); err == nil {
		return b, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	return fs.ReadFile(o.lower, name)
}
