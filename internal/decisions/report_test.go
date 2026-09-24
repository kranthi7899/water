package decisions

import (
	"strings"
	"testing"
	"time"
)

// TestCardHTMLReportRendersEveryField proves internal/reports is actually
// reachable from a real card, not dead code: every field the card carries
// (question, deadline, evidence with its source, a sourced figure, options,
// gaps, recommendation) must appear in the rendered HTML, and nothing is
// invented beyond what the card already has.
func TestCardHTMLReportRendersEveryField(t *testing.T) {
	deadline := time.Date(2026, 10, 1, 17, 0, 0, 0, time.UTC)
	c := &Card{
		Lead:           "Budget request: Q3 laptop",
		Question:       "Approve the $1,500 laptop purchase?",
		Deadline:       &deadline,
		Evidence:       []Evidence{{Text: "Dana asked for a new laptop", Source: "gmail:m1"}},
		Defaults:       map[string]any{"runway_months": 8.2},
		DefaultSources: map[string]string{"runway_months": "code:compute_runway"},
		Options:        []Option{{Label: "Approve", Consequences: "one-time $1,500 spend"}},
		Gaps:           []string{"comparable past requests not found"},
		Recommendation: "Approve: well under the $2,000 threshold.",
	}
	html, err := c.HTMLReport("")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Budget request: Q3 laptop", // title falls back to Lead
		"Approve the $1,500 laptop purchase?",
		"Dana asked for a new laptop", "gmail:m1",
		"runway_months", "8.2", "code:compute_runway",
		"Approve", "one-time $1,500 spend",
		"comparable past requests not found",
		"Approve: well under the $2,000 threshold.",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("rendered report is missing %q:\n%s", want, html)
		}
	}
	if !strings.HasPrefix(html, "<!doctype html>") {
		t.Fatal("HTMLReport did not return a full HTML document")
	}
}

// TestCardHTMLReportEscapesUntrustedContent confirms a card built from
// external evidence (a hostile email subject, say) cannot inject markup
// into the report -- internal/reports' own html/template contract, verified
// again here through the actual Card path a caller uses.
func TestCardHTMLReportEscapesUntrustedContent(t *testing.T) {
	c := &Card{
		Lead:      "generic",
		Question:  "<script>alert(1)</script>",
		Untrusted: true,
	}
	html, err := c.HTMLReport("")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(html, "<script>") {
		t.Fatalf("untrusted content was not escaped: %s", html)
	}
}

// TestCardHTMLReportTitleFallsBackToLead is the explicit title-argument
// path a caller (the gateway's email-decision-report handler) uses when it
// wants the mail's own subject as the report's title instead.
func TestCardHTMLReportTitleFallsBackToLead(t *testing.T) {
	c := &Card{Lead: "the lead", Question: "q"}
	withTitle, err := c.HTMLReport("Custom subject")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withTitle, "Custom subject") || strings.Contains(withTitle, "<title>the lead") {
		t.Fatalf("explicit title was not used: %s", withTitle)
	}
	withoutTitle, err := c.HTMLReport("")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(withoutTitle, "the lead") {
		t.Fatalf("empty title should fall back to Lead: %s", withoutTitle)
	}
}
