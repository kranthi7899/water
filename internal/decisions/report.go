package decisions

import (
	"fmt"

	"water/internal/reports"
)

// HTMLReport renders c as a self-contained HTML report (internal/reports),
// suitable as an email attachment: every section comes straight from the
// card's own fields, which Validate already requires to be sourced -- this
// adds no new numbers or prose of its own, it only lays out what the card
// already carries. title, when empty, falls back to c.Lead.
func (c *Card) HTMLReport(title string) (string, error) {
	if title == "" {
		title = c.Lead
	}
	r := reports.Report{Title: title}

	r.Sections = append(r.Sections, reports.Section{Heading: "Question", Paragraphs: []string{c.Question}})

	if c.Deadline != nil {
		r.Sections = append(r.Sections, reports.Section{Heading: "Deadline", Paragraphs: []string{deadline(c.Deadline)}})
	}

	if len(c.Evidence) > 0 {
		t := &reports.Table{Headers: []string{"Evidence", "Source"}}
		for _, e := range c.Evidence {
			t.Rows = append(t.Rows, []string{e.Text, e.Source})
		}
		r.Sections = append(r.Sections, reports.Section{Heading: "Evidence", Table: t})
	}

	if len(c.Defaults) > 0 {
		t := &reports.Table{Headers: []string{"Figure", "Value", "Source"}}
		for _, k := range sortedKeys(c.Defaults) {
			t.Rows = append(t.Rows, []string{k, fmtValue(c.Defaults[k]), c.DefaultSources[k]})
		}
		r.Sections = append(r.Sections, reports.Section{Heading: "Figures", Table: t})
	}

	if len(c.Options) > 0 {
		paras := make([]string, len(c.Options))
		for i, o := range c.Options {
			p := o.Label
			if o.Consequences != "" {
				p += " — " + o.Consequences
			}
			paras[i] = p
		}
		r.Sections = append(r.Sections, reports.Section{Heading: "Options", Paragraphs: paras})
	}

	if len(c.Gaps) > 0 {
		r.Sections = append(r.Sections, reports.Section{Heading: "Gaps", Paragraphs: c.Gaps})
	}

	if c.Recommendation != "" {
		r.Sections = append(r.Sections, reports.Section{Heading: "Recommendation", Paragraphs: []string{c.Recommendation}})
	}

	return reports.Render(r)
}

func fmtValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return clip(fmt.Sprint(v), 200)
}
