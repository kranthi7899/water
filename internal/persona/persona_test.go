package persona

import (
	"io/fs"
	"testing"
	"testing/fstest"
)

func TestFrontmatterAndBlank(t *testing.T) {
	d, err := ParseDoc([]byte("---\nschema: 1\nstatus: unwritten\n---\n<!-- x -->\n"))
	if err != nil || d.Front["schema"] != 1 || !d.IsBlank() {
		t.Fatalf("%+v %v", d, err)
	}
	d, _ = ParseDoc([]byte("---\nname: n\n---\nbody text\n"))
	if d.Prose() != "body text" || d.IsBlank() {
		t.Fatalf("%+v", d)
	}
	d, _ = ParseDoc([]byte("no front\n"))
	if d.Prose() != "no front" {
		t.Fatalf("%+v", d)
	}
}

func TestOverlayShadowsPerFile(t *testing.T) {
	lower := fstest.MapFS{
		"ceo/role.yaml": &fstest.MapFile{Data: []byte("lower")},
		"ceo/soul.md":   &fstest.MapFile{Data: []byte("lower soul")},
	}
	upper := fstest.MapFS{
		"ceo/soul.md":   &fstest.MapFile{Data: []byte("upper soul")},
		"ops/role.yaml": &fstest.MapFile{Data: []byte("upper")},
	}
	o := NewOverlay(NewEmbedded(upper), NewEmbedded(lower)).FS()
	if b, _ := readAll(o, "ceo/soul.md"); string(b) != "upper soul" {
		t.Fatalf("upper should shadow: %q", b)
	}
	if b, _ := readAll(o, "ceo/role.yaml"); string(b) != "lower" {
		t.Fatalf("lower should fall through: %q", b)
	}
	ents, err := fs.ReadDir(o, ".")
	if err != nil || len(ents) != 2 {
		t.Fatalf("union listing: %v %v", ents, err)
	}
}

func readAll(fsys fs.FS, name string) ([]byte, error) { return fs.ReadFile(fsys, name) }

func TestSkillsAndSelector(t *testing.T) {
	fsys := fstest.MapFS{
		"ceo/skills/capital-allocation/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: Capital Allocation\nkeywords: [budget, allocate]\n---\nprocedure\n")},
		"ceo/skills/crisis/SKILL.md":             &fstest.MapFile{Data: []byte("---\nname: Crisis Triage\n---\ntriage\n")},
	}
	sk, err := DiscoverSkills(fsys, "ceo")
	if err != nil || len(sk) != 2 {
		t.Fatalf("%v %v", sk, err)
	}
	got := KeywordSelector{}.Select("please allocate the Q3 budget", sk)
	if len(got) != 1 || got[0].Slug != "capital-allocation" {
		t.Fatalf("selected %v", got)
	}
	got = KeywordSelector{}.Select("there is a crisis", sk)
	if len(got) != 1 || got[0].Slug != "crisis" {
		t.Fatalf("selected %v", got)
	}
}

func TestIndexLookup(t *testing.T) {
	fsys := fstest.MapFS{"ceo/.index.json": &fstest.MapFile{Data: []byte(`{"schema":1,"role":"ceo","entries":[{"sentence":"We cut burn by a third in 2019.","source_id":"rec-42","source":"board-minutes-2019-q4"}]}`)}}
	idx, err := LoadIndex(fsys, "ceo")
	if err != nil {
		t.Fatal(err)
	}
	if hits := idx.Lookup("cut burn by a third"); len(hits) != 1 || hits[0].SourceID != "rec-42" {
		t.Fatalf("lookup %v", hits)
	}
}
