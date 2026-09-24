package reports

import (
	"strings"
	"testing"
)

func TestRenderParagraphsAndTable(t *testing.T) {
	r := Report{
		Title: "Weekly Brief",
		Sections: []Section{
			{
				Heading:    "Summary",
				Paragraphs: []string{"Everything is on track.", "No blockers."},
			},
			{
				Heading: "Deals",
				Table: &Table{
					Headers: []string{"Name", "Stage", "Amount"},
					Rows: [][]string{
						{"Acme Co", "Negotiation", "$12,000"},
						{"Globex", "Closed Won", "$4,500"},
					},
				},
			},
		},
	}

	out, err := Render(r)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "<!doctype html>") {
		t.Fatalf("output does not start with doctype: %q", out[:40])
	}
	if !strings.Contains(out, "<html") || !strings.Contains(out, "</html>") {
		t.Fatalf("output is not a full html document:\n%s", out)
	}
	for _, want := range []string{
		"Weekly Brief", "Summary", "Everything is on track.", "No blockers.",
		"Deals", "Acme Co", "Negotiation", "$12,000", "Globex",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q", want)
		}
	}
	if strings.Count(out, "<table") != 1 {
		t.Errorf("expected exactly one table, got %d:\n%s", strings.Count(out, "<table"), out)
	}
}

func TestRenderEscapesUntrustedContent(t *testing.T) {
	evil := `<script>alert('pwned')</script>`
	r := Report{
		Title: "Inbox Digest",
		Sections: []Section{
			{
				Heading:    "From external sender",
				Paragraphs: []string{evil},
				Table: &Table{
					Headers: []string{evil},
					Rows:    [][]string{{evil}},
				},
			},
		},
	}

	out, err := Render(r)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.Contains(out, "<script>alert('pwned')</script>") {
		t.Fatalf("untrusted content was not escaped, found raw script tag:\n%s", out)
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Errorf("expected escaped script tag in output, got:\n%s", out)
	}
}

func TestRenderEmptyReport(t *testing.T) {
	out, err := Render(Report{})
	if err != nil {
		t.Fatalf("Render on empty report returned error: %v", err)
	}
	if !strings.Contains(out, "<!doctype html>") {
		t.Fatalf("empty report did not render a valid document:\n%s", out)
	}
	if !strings.Contains(out, "<html") || !strings.Contains(out, "</html>") {
		t.Fatalf("empty report is not a full html document:\n%s", out)
	}
}

func TestRenderIncludesExactPaletteHexValues(t *testing.T) {
	out, err := Render(Report{Title: "Palette Check"})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	for _, hex := range []string{
		"#050505", // ink
		"#0B52A8", // cobalt
		"#ECE8D8", // cream
		"#D81E05", // signal red
		"#25E737", // terminal green
		"#2A2D33", // graphite
		"#7E7E7E", // hairline
		"#C9A227", // gold
		"#3E9B4F", // moss
	} {
		if !strings.Contains(out, hex) {
			t.Errorf("output CSS missing palette hex value %s", hex)
		}
	}
}
