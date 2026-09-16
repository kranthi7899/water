package sitebuild

import (
	"strings"
	"testing"
)

// TestParsePlanToleratesFences — models wrap JSON in prose or a code fence;
// the object is still found.
func TestParsePlanToleratesFences(t *testing.T) {
	for _, raw := range []string{
		"```json\n{\"title\":\"T\",\"sections\":[{\"id\":\"hero\",\"heading\":\"H\"}]}\n```",
		"Here is the plan:\n{\"title\":\"T\",\"sections\":[{\"id\":\"hero\",\"heading\":\"H\"}]}\nHope that helps.",
		"{\"title\":\"T\",\"sections\":[{\"id\":\"hero\",\"heading\":\"H\"}]}",
	} {
		p, err := ParsePlan(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if p.Title != "T" {
			t.Fatalf("title: %q", p.Title)
		}
	}
}

// TestParsePlanFailsLoudly — unparseable output is an error, never a guess
// assembled from prose.
func TestParsePlanFailsLoudly(t *testing.T) {
	for _, raw := range []string{
		"",
		"I could not produce JSON.",
		"{not json at all}",
		`{"title":"","sections":[{"id":"hero"}]}`,
		`{"title":"T","sections":[]}`,
	} {
		if _, err := ParsePlan(raw); err == nil {
			t.Fatalf("parse of %q should have failed", raw)
		}
	}
}

// TestNormalizeSlugsAndDedupesIDs — ids become slugs and stay unique, so they
// are safe as HTML anchors and unambiguous as template slots.
func TestNormalizeSlugsAndDedupesIDs(t *testing.T) {
	p := Normalize(SitePlan{
		Title: "T",
		Sections: []Section{
			{ID: "Hero Section!", Heading: "a"},
			{ID: "hero section", Heading: "b"},
			{ID: "", Heading: "Fallback Heading"},
		},
	})
	got := []string{p.Sections[0].ID, p.Sections[1].ID, p.Sections[2].ID}
	want := []string{"hero-section", "hero-section-2", "fallback-heading"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ids: got %v want %v", got, want)
		}
	}
}

// TestNormalizeStripsControlCharacters — page copy is echoed back in the
// approval plan, so it must not be able to rewrite the terminal.
func TestNormalizeStripsControlCharacters(t *testing.T) {
	p := Normalize(SitePlan{
		Title:    "clean\x1b[2Jtitle",
		Sections: []Section{{ID: "hero", Heading: "h\x00i", Body: "line\nbreak"}},
	})
	if strings.ContainsAny(p.Title, "\x1b\x00") || strings.ContainsAny(p.Sections[0].Heading, "\x1b\x00") {
		t.Fatalf("control characters survived: %+v", p)
	}
	if strings.Contains(p.Sections[0].Body, "\n") {
		t.Fatalf("newline survived in body: %q", p.Sections[0].Body)
	}
}

// TestNormalizeClampsRunawayContent — a plan cannot grow without bound.
func TestNormalizeClampsRunawayContent(t *testing.T) {
	var secs []Section
	for i := 0; i < maxSections*3; i++ {
		secs = append(secs, Section{ID: "s", Body: strings.Repeat("x", maxTextBytes*2)})
	}
	p := Normalize(SitePlan{Title: "T", Sections: secs})
	if len(p.Sections) > maxSections {
		t.Fatalf("sections not clamped: %d", len(p.Sections))
	}
	for _, s := range p.Sections {
		if len(s.Body) > maxTextBytes+4 {
			t.Fatalf("body not clamped: %d", len(s.Body))
		}
	}
}

// TestBuildPromptCarriesBothInputs — the extraction turn sees the brief and
// the CEO's synthesis, and asks for JSON only.
func TestBuildPromptCarriesBothInputs(t *testing.T) {
	got := BuildPrompt("make a landing page", "the council decided X")
	for _, want := range []string{
		"make a landing page",
		"the council decided X",
		"<brief>",
		"<final_synthesis>",
		"ONLY a single JSON object",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
}
