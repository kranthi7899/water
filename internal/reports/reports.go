// Package reports renders themed, self-contained HTML reports for the CEO
// twin.
//
// Contract: this package is a pure renderer. Every value that ends up in a
// Report — every Title, Heading, paragraph, table header and cell — must
// already be sourced by the caller before it reaches Render: fetched from a
// connector, computed locally, or written by a human. reports does not
// fetch data, call a model, retry a network request, or invent facts of its
// own; it only lays out what it is given. This matches the project's
// sourced-numbers-only posture.
//
// Because a report can summarize data that includes External (untrusted)
// text — a Gmail subject line, a HubSpot deal name, and so on — Render uses
// html/template, never text/template, so every caller-supplied string is
// HTML-escaped on the way into the document. The output is a single
// self-contained HTML page: inline <style> only, no external stylesheets,
// fonts, scripts or images, so it renders correctly as an email attachment
// with no network access.
package reports

import (
	"bytes"
	"html/template"
)

// Report is the full document to render.
type Report struct {
	// Title is shown in the red-rule-over-cobalt title block and in the
	// document's <title>.
	Title string
	// Sections are rendered in order.
	Sections []Section
}

// Section is one block of a Report: a heading followed by either free-form
// paragraphs or a single table. Set at most one of Paragraphs or Table; if
// both are set, both are rendered (paragraphs first), but callers should
// keep sections simple — this is a renderer, not a page-layout engine.
type Section struct {
	Heading    string
	Paragraphs []string
	// Table is optional. Leave nil for a paragraphs-only section.
	Table *Table
}

// Table is a simple headers-and-rows grid. Every row is expected to have
// the same number of cells as Headers; Render does not validate this.
type Table struct {
	Headers []string
	Rows    [][]string
}

var tmpl = template.Must(template.New("report").Parse(reportTemplate))

// Render turns r into a single self-contained HTML document styled with
// the Water design kit (docs/design-kit/water.md): a cream page, ink text,
// cobalt headings, and a signal-red rule over a cobalt title block. All
// text in r is HTML-escaped, so untrusted content cannot break out of the
// page or execute as script.
func Render(r Report) (string, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, r); err != nil {
		return "", err
	}
	return buf.String(), nil
}

const reportTemplate = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>{{.Title}}</title>
<style>
  :root {
    --ink: #050505;
    --cobalt: #0B52A8;
    --cream: #ECE8D8;
    --signal-red: #D81E05;
    --terminal-green: #25E737;
    --graphite: #2A2D33;
    --hairline: #7E7E7E;
    --gold: #C9A227;
    --moss: #3E9B4F;
  }
  body {
    margin: 0;
    background: var(--cream);
    color: var(--ink);
    font-family: "Helvetica Neue", Helvetica, Arial, sans-serif;
    font-weight: 400;
    line-height: 1.5;
  }
  .report {
    max-width: 720px;
    margin: 0 auto;
    padding-bottom: 40px;
  }
  .title-rule {
    height: 4px;
    background: var(--signal-red);
  }
  .title-block {
    background: var(--cobalt);
    color: var(--cream);
    padding: 28px 32px;
  }
  .title-block h1 {
    margin: 0;
    font-family: "Helvetica Neue", Helvetica, Arial, sans-serif;
    font-weight: 800;
    letter-spacing: -0.01em;
    font-size: 34px;
    text-transform: uppercase;
  }
  .section {
    padding: 24px 32px;
    border-bottom: 1px solid var(--hairline);
  }
  .section:last-child {
    border-bottom: none;
  }
  .section h2 {
    margin: 0 0 12px 0;
    color: var(--cobalt);
    font-family: "Helvetica Neue", Helvetica, Arial, sans-serif;
    font-weight: 800;
    font-size: 18px;
  }
  .section p {
    margin: 0 0 12px 0;
    color: var(--ink);
  }
  .section p:last-child {
    margin-bottom: 0;
  }
  table {
    width: 100%;
    border-collapse: collapse;
  }
  thead th {
    text-align: left;
    background: var(--graphite);
    color: var(--cream);
    font-family: "SF Mono", Menlo, Monaco, Consolas, monospace;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    font-size: 11px;
    padding: 8px 10px;
  }
  tbody td {
    padding: 8px 10px;
    border-bottom: 1px solid var(--hairline);
    font-size: 14px;
  }
  tbody tr:last-child td {
    border-bottom: none;
  }
</style>
</head>
<body>
  <div class="report">
    <div class="title-rule"></div>
    <div class="title-block">
      <h1>{{.Title}}</h1>
    </div>
    {{range .Sections}}
    <div class="section">
      {{if .Heading}}<h2>{{.Heading}}</h2>{{end}}
      {{range .Paragraphs}}<p>{{.}}</p>
      {{end}}
      {{if .Table}}
      <table>
        <thead>
          <tr>
            {{range .Table.Headers}}<th>{{.}}</th>{{end}}
          </tr>
        </thead>
        <tbody>
          {{range .Table.Rows}}
          <tr>
            {{range .}}<td>{{.}}</td>{{end}}
          </tr>
          {{end}}
        </tbody>
      </table>
      {{end}}
    </div>
    {{end}}
  </div>
</body>
</html>
`
