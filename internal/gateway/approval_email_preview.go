package gateway

import (
	"errors"
	"html"
	"net/http"
	"strings"

	"water"
	"water/internal/approvals"
	"water/internal/brand"
)

// previewCSP is the Content-Security-Policy meta tag handleApprovalEmailPreview
// splices into the head of the rendered email before handing it to the
// client (docs/slices/BRAND.md task 8's "Preview full email"): the
// sandboxed <iframe> the client renders this into already has no
// allow-scripts/allow-same-origin/allow-popups/allow-forms tokens (see
// view_approvals.js), which is what stops script execution and top-level
// navigation, but the sandbox attribute alone does NOT stop the document
// from attempting to fetch a network resource (an <img src> or a CSS
// background) -- this CSP is that second, independent layer. img-src
// 'none' means the template's cid: references (which cannot resolve
// outside a real email client anyway) fail closed by policy rather than
// merely by an unrecognized scheme, and default-src 'none' denies
// everything else (scripts, connects, frames, objects) this document has
// no legitimate reason to load. style-src 'unsafe-inline' is the one
// allowance: the template styles every element with a plain style="..."
// attribute (no <style> tag, no external stylesheet), and CSS alone,
// with img-src/connect-src both 'none', has no exfiltration path left.
const previewCSP = `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src 'none'; style-src 'unsafe-inline'; font-src 'none'; connect-src 'none'; frame-src 'none'; object-src 'none'; script-src 'none'; base-uri 'none'; form-action 'none'">`

// handleApprovalEmailPreview serves GET /v1/approvals/{id}/preview
// (docs/slices/BRAND.md task 8): the "Preview full email" control on a
// gmail.send_message approval card renders the FINAL email -- the exact
// output of internal/brand.RenderEmail for this envelope's own payload and
// the twin's own loaded Signature, the same call the send path itself
// makes -- rather than a mockup. Any action other than "gmail.send_message"
// answers 404: there is nothing brand-templated to preview.
//
// The payload's "body" is today's plain, caller-drafted text, not yet the
// sanitized HTML the send path's own sanitizer (a parallel, separate
// change this task does not touch) will eventually produce.
// previewBodyHTML converts it to the same minimal p/br markup a sanitized
// plain body would produce, escaping every character first so an embedded
// "<" or "&" can never be misread as markup. Once that sanitizer lands,
// wiring this preview to call it instead of previewBodyHTML is that
// change's job, not this one's.
//
// A twin with no twins/<id>/brand/signature.yaml yet (see
// addBrandPayloadFields' identical case in brand_payload.go) answers 409:
// there is a real envelope to preview, but no signature configured to
// render it with.
func (d *Daemon) handleApprovalEmailPreview(w http.ResponseWriter, r *http.Request) {
	if err := d.cfg.Approvals.ExpireStale(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	e, err := d.cfg.Approvals.Get(r.Context(), r.PathValue("id"))
	if errors.Is(err, approvals.ErrNotFound) {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if e.Action != "gmail.send_message" {
		http.Error(w, "approval "+e.ID+" is not an email; there is nothing to preview", http.StatusNotFound)
		return
	}
	sig, err := brand.LoadSignature(water.TwinsFS(), "twins/"+d.cfg.Manifest.ID+"/brand/signature.yaml")
	if err != nil {
		http.Error(w, "no brand signature is configured for this twin yet: "+err.Error(), http.StatusConflict)
		return
	}
	htmlOut, _, err := brand.RenderEmail(previewBodyHTML(bodyOf(e.Payload)), *sig)
	if err != nil {
		http.Error(w, "rendering the preview: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"html": withPreviewCSP(htmlOut)})
}

// withPreviewCSP splices previewCSP as the first child of <head> in a
// rendered email document (internal/brand.RenderEmail's own output always
// opens with a literal "<head>" -- see templates/email-template.html). A
// document with no recognizable <head> (never true for the real template,
// but defensive rather than assumed) gets the CSP prepended to the whole
// string instead, so a preview is never served without it.
func withPreviewCSP(htmlOut string) string {
	const head = "<head>"
	if i := strings.Index(htmlOut, head); i >= 0 {
		return htmlOut[:i+len(head)] + previewCSP + htmlOut[i+len(head):]
	}
	return previewCSP + htmlOut
}

// previewBodyHTML turns a plain-text draft body into the minimal safe HTML
// internal/brand.RenderEmail expects (see this file's package doc above):
// every character escaped first, then reflowed into <p> paragraphs (blank
// lines) and <br> line breaks (single newlines).
func previewBodyHTML(body string) string {
	body = strings.ReplaceAll(strings.TrimSpace(body), "\r\n", "\n")
	if body == "" {
		return ""
	}
	var out strings.Builder
	for i, para := range strings.Split(body, "\n\n") {
		if i > 0 {
			out.WriteString("\n")
		}
		out.WriteString("<p>")
		lines := strings.Split(para, "\n")
		for j, line := range lines {
			if j > 0 {
				out.WriteString("<br>")
			}
			out.WriteString(html.EscapeString(line))
		}
		out.WriteString("</p>")
	}
	return out.String()
}
