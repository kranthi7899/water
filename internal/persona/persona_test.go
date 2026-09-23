package persona

import (
	"io/fs"
	"strings"
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
		"ceo/skills/capital-allocation/SKILL.md": &fstest.MapFile{Data: []byte("---\nname: capital-allocation\ndescription: Decides how to allocate a budget between competing uses.\nkeywords: [budget, allocate]\n---\nprocedure\n")},
		"ceo/skills/crisis/SKILL.md":             &fstest.MapFile{Data: []byte("---\nname: crisis\ndescription: Triages a crisis that threatens public trust.\n---\ntriage\n")},
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

func TestSkillSchemaRejects(t *testing.T) {
	bad := map[string]string{
		"Name":       "---\nname: Capital\ndescription: ok here\n---\nbody\n",
		"reserved":   "---\nname: claude-helper\ndescription: ok here\n---\nbody\n",
		"no-desc":    "---\nname: x-y\n---\nbody\n",
		"angle-tags": "---\nname: x-y\ndescription: ok here\n---\n<instructions>body</instructions>\n",
		"first-pers": "---\nname: x-y\ndescription: I help with things\n---\nbody\n",
	}
	for k, v := range bad {
		slug := "x-y"
		if k == "Name" {
			slug = "capital"
		}
		if k == "reserved" {
			slug = "claude-helper"
		}
		fsys := fstest.MapFS{"r/skills/" + slug + "/SKILL.md": &fstest.MapFile{Data: []byte(v)}}
		if _, err := DiscoverSkills(fsys, "r"); err == nil {
			t.Errorf("%s: expected schema violation", k)
		}
	}
}

func TestDescriptionSelector(t *testing.T) {
	skills := []Skill{
		{Slug: "reference-class-forecasting", Name: "reference-class-forecasting", Description: "Corrects a schedule or cost estimate by comparing it with how similar past efforts actually turned out."},
		{Slug: "pre-mortem", Name: "pre-mortem", Description: "Surfaces plausible failure reasons before committing to a hard-to-reverse plan."},
	}
	sel := DescriptionSelector{}
	got := sel.Select("our bottom-up cost estimate for the migration schedule looks optimistic compared with past efforts", skills)
	if len(got) != 1 || got[0].Slug != "reference-class-forecasting" {
		t.Fatalf("selected %v", got)
	}
	if got := sel.Select("hello there", skills); len(got) != 0 {
		t.Fatalf("unrelated task selected %v", got)
	}
}

// Reasoning refers to "the Experience section above", so it must render after
// Experience and before any skill; a role without reasoning.md still loads.
func TestReasoningRendersAfterExperience(t *testing.T) {
	fsys := fstest.MapFS{
		"ceo/soul.md":       &fstest.MapFile{Data: []byte("the soul")},
		"ceo/experience.md": &fstest.MapFile{Data: []byte("the lessons")},
		"ceo/reasoning.md":  &fstest.MapFile{Data: []byte("the steps")},
	}
	p, err := Load(fsys, "ceo", "ceo", Identity{})
	if err != nil {
		t.Fatal(err)
	}
	out := p.Render([]Skill{{Name: "s", Body: "skill body"}})
	e, r, s := strings.Index(out, "# Experience"), strings.Index(out, "# Reasoning\n\nthe steps"), strings.Index(out, "# Skill: s")
	if e < 0 || r < 0 || s < 0 || !(e < r && r < s) {
		t.Fatalf("section order wrong:\n%s", out)
	}

	delete(fsys, "ceo/reasoning.md")
	p, err = Load(fsys, "ceo", "ceo", Identity{})
	if err != nil {
		t.Fatalf("missing reasoning.md must be tolerated: %v", err)
	}
	if !p.Reasoning.IsBlank() || strings.Contains(p.Render(nil), "# Reasoning") {
		t.Fatalf("missing reasoning.md must render nothing: %q", p.Render(nil))
	}
}

// Souls weight lessons "by how many independent cases support it"; Render
// must surface that count (distinct source_ids, exact sentence match) and
// never a source name.
func TestExperienceShowsCorroborationCount(t *testing.T) {
	fsys := fstest.MapFS{
		"ceo/experience.md": &fstest.MapFile{Data: []byte("Preamble.\n\nLesson  one.\n\nLesson two.\n\nUnindexed lesson.\n")},
		"ceo/.index.json": &fstest.MapFile{Data: []byte(`{"schema":1,"role":"ceo","entries":[
			{"sentence":"lesson one.","source_id":"A","source":"Secret Corp 2001"},
			{"sentence":"Lesson one.","source_id":"B","source":"Other Inc"},
			{"sentence":"Lesson one.","source_id":"B","source":"Other Inc duplicate"},
			{"sentence":"Lesson two.","source_id":"C","source":"Third Co"},
			{"sentence":"Lesson","source_id":"D","source":"partial"}]}`)},
	}
	p, err := Load(fsys, "ceo", "ceo", Identity{})
	if err != nil {
		t.Fatal(err)
	}
	out := p.Render(nil)
	for _, want := range []string{"Preamble.\n\n", "Lesson  one. (2 independent cases)", "Lesson two. (1 independent case)", "Unindexed lesson."} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Unindexed lesson. (") || strings.Contains(out, "Corp") || strings.Contains(out, "Inc") {
		t.Fatalf("unexpected annotation or source leak:\n%s", out)
	}
}
