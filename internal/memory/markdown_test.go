package memory

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func TestMarkdownRoundTrip(t *testing.T) {
	seed := fstest.MapFS{"ceo/memory/session.md": &fstest.MapFile{Data: []byte("---\nschema: 1\nrole: ceo\n---\n<!-- seeded -->\n")}}
	m, err := NewMarkdown(Options{Root: t.TempDir(), Seed: seed})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := m.Add(ctx, "ceo", Entry{Text: "first\nline two", Tags: []string{"a", "b"}}); err != nil {
		t.Fatal(err)
	}
	if err := m.Add(ctx, "ceo", Entry{ID: "m_fixed", Text: "second"}); err != nil {
		t.Fatal(err)
	}
	es, err := m.Snapshot(ctx, "ceo")
	if err != nil || len(es) != 2 {
		t.Fatalf("snapshot %v %v", es, err)
	}
	if es[0].Text != "first\nline two" || len(es[0].Tags) != 2 || es[1].ID != "m_fixed" {
		t.Fatalf("bad parse: %+v", es)
	}
	raw, _ := os.ReadFile(m.Path("ceo"))
	if !strings.Contains(string(raw), "<!-- seeded -->") {
		t.Fatal("seed preamble lost")
	}
	if err := m.Replace(ctx, "ceo", "m_fixed", Entry{Text: "second v2"}); err != nil {
		t.Fatal(err)
	}
	if err := m.Remove(ctx, "ceo", es[0].ID); err != nil {
		t.Fatal(err)
	}
	es, _ = m.Snapshot(ctx, "ceo")
	if len(es) != 1 || es[0].Text != "second v2" {
		t.Fatalf("after edits: %+v", es)
	}
	if err := m.Remove(ctx, "ceo", "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove missing: %v", err)
	}
}

func TestBoundsAreExplicitErrors(t *testing.T) {
	m, _ := NewMarkdown(Options{Root: t.TempDir(), Limits: Limits{MaxEntries: 2, MaxBytes: 100000}})
	ctx := context.Background()
	_ = m.Add(ctx, "cfo", Entry{Text: "1"})
	_ = m.Add(ctx, "cfo", Entry{Text: "2"})
	if err := m.Add(ctx, "cfo", Entry{Text: "3"}); !errors.Is(err, ErrBoundsExceeded) {
		t.Fatalf("want ErrBoundsExceeded, got %v", err)
	}
	es, _ := m.Snapshot(ctx, "cfo")
	if len(es) != 2 {
		t.Fatalf("silent truncation or growth: %d", len(es))
	}
	// Byte ceiling too.
	m2, _ := NewMarkdown(Options{Root: t.TempDir(), Limits: Limits{MaxEntries: 100, MaxBytes: 80}})
	if err := m2.Add(ctx, "cfo", Entry{Text: strings.Repeat("x", 100)}); !errors.Is(err, ErrBoundsExceeded) {
		t.Fatalf("want byte bound error, got %v", err)
	}
	// Prune brings an over-limit file back under.
	m3, _ := NewMarkdown(Options{Root: t.TempDir(), Limits: Limits{MaxEntries: 5}})
	for i := 0; i < 5; i++ {
		_ = m3.Add(ctx, "cto", Entry{Text: "e"})
	}
	m3.limits.MaxEntries = 3
	if _, err := m3.Snapshot(ctx, "cto"); !errors.Is(err, ErrBoundsExceeded) {
		t.Fatalf("snapshot over limit should error, got %v", err)
	}
	removed, err := m3.Prune(ctx, "cto")
	if err != nil || len(removed) != 2 {
		t.Fatalf("prune: %v %v", removed, err)
	}
}
