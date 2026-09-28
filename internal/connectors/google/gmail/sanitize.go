package gmail

import (
	"html"
	"net/url"
	"regexp"
	"strings"
)

// allowedTags is the body sanitizer's exact allowlist (docs/slices/BRAND.md
// task 7), matching internal/brand.RenderEmail/htmlToText's own documented
// contract for what a sanitized body may contain: p, br, a, strong, em, ul,
// ol, li. Nothing else survives sanitizeBody, tag or attribute.
var allowedTags = map[string]bool{
	"p": true, "br": true, "a": true, "strong": true, "em": true,
	"ul": true, "ol": true, "li": true,
}

// allowedLinkSchemes is the exact set of URL schemes a sanitized <a href>
// may use. Anything else (javascript:, data:, file:, a scheme-less/
// relative URL, ...) is dropped.
var allowedLinkSchemes = map[string]bool{"http": true, "https": true, "mailto": true}

// stripElements are tags whose entire element -- opening tag, content and
// closing tag -- is removed, never just unwrapped: their content is not
// safe body text (script is code; style is CSS that could otherwise smuggle
// display-affecting or exfiltrating content past the allowlist).
var stripElements = map[string]bool{"script": true, "style": true}

// tagRe matches one HTML tag's angle-bracket span, capturing everything
// between < and > as a single group for parseTag to split. It deliberately
// does not try to parse quoted attribute values that themselves contain
// '>' -- body text is model-drafted, simple markup, never expected to carry
// that.
var tagRe = regexp.MustCompile(`(?s)<([^<>]*)>`)

// hrefRe extracts an href attribute's value (double-quoted, single-quoted,
// or bare) from a tag's raw attribute string.
var hrefRe = regexp.MustCompile(`(?is)\bhref\s*=\s*("([^"]*)"|'([^']*)'|([^\s"'>]+))`)

// sanitizeBody is the one sanitize pass between a model-drafted body and
// internal/brand.RenderEmail (docs/slices/BRAND.md task 7): it is called
// exactly once, and both the HTML and plain-text parts RenderEmail produces
// are derived from its single output, so there is never a second,
// differently-sanitized copy of the body.
//
// A body with no '<' at all is treated as plain text (the write schema's
// own description of "body") and turned into blank-line-separated <p>
// paragraphs with single newlines as <br>, so a plain multi-line body still
// reads as more than one run-on line once it reaches an HTML mail client.
// A body that already contains markup is run through the tag allowlist
// directly instead: script/style elements are dropped entirely (tag and
// content), img tags are dropped (void, no safe content to keep), any other
// disallowed tag is unwrapped (its own tags dropped, inner text kept), and
// every allowed tag has every attribute stripped except <a>'s href, which
// is kept only when its scheme is http, https or mailto.
func sanitizeBody(in string) string {
	if !strings.Contains(in, "<") {
		return plainTextToParagraphs(in)
	}
	return filterAllowedTags(in)
}

// plainTextToParagraphs turns plain text (no HTML markup at all) into safe
// <p>/<br> markup: text is HTML-escaped, blank lines separate paragraphs,
// and a single newline within a paragraph becomes <br>.
func plainTextToParagraphs(in string) string {
	in = strings.ReplaceAll(in, "\r\n", "\n")
	paras := strings.Split(strings.TrimSpace(in), "\n\n")
	var out strings.Builder
	for _, p := range paras {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		escaped := html.EscapeString(p)
		escaped = strings.ReplaceAll(escaped, "\n", "<br>")
		out.WriteString("<p>")
		out.WriteString(escaped)
		out.WriteString("</p>")
	}
	return out.String()
}

// filterAllowedTags walks in tag by tag (tagRe), keeping only allowedTags
// (attribute-stripped, except a validated <a href>), dropping stripElements
// entirely including their content, dropping img outright, and unwrapping
// (tag removed, text kept) anything else -- see sanitizeBody's doc comment.
func filterAllowedTags(in string) string {
	var out strings.Builder
	last := 0
	skipDepth := 0
	skipTag := ""

	for _, m := range tagRe.FindAllStringSubmatchIndex(in, -1) {
		text := in[last:m[0]]
		content := in[m[2]:m[3]]
		last = m[1]

		name, attrs, closing, selfClose := parseTag(content)

		if skipDepth > 0 {
			if name == skipTag {
				switch {
				case closing:
					skipDepth--
				case !selfClose:
					skipDepth++
				}
			}
			continue // drop text and tag alike while inside a stripped element
		}

		// text is HTML-escaped, never written raw. tagRe's [^<>]* can't
		// span an embedded '<' (e.g. an attribute value smuggling a nested
		// tag, or a truncated tag with no matching '>' nearby), so the
		// *intended* match for a given '<' can fail and the scan instead
		// matches a later, incidental "<...>" span elsewhere in the input
		// -- everything in between becomes "text" here, including
		// fragments of what was meant to be a single dangerous tag (found
		// live: `<a href="ht<script>x</script>tp://evil" onclick="alert(1)">`
		// left `<a href="ht` and `tp://evil" onclick="alert(1)">` as two
		// separate "text" spans that, written raw, silently reassembled
		// into a working onclick handler -- sanitizedHref's scheme check
		// was never even reached, since this text never re-enters tag
		// parsing). Escaping closes the class outright: any leaked '<'/'>'
		// renders as inert text instead of ever being reinterpreted as
		// markup by a downstream HTML parser.
		out.WriteString(html.EscapeString(text))

		if stripElements[name] {
			if !closing && !selfClose {
				skipDepth = 1
				skipTag = name
			}
			continue
		}

		if name == "img" {
			continue // void, always dropped
		}

		if !allowedTags[name] {
			continue // unwrap: drop the tag markers, keep surrounding text
		}

		if closing {
			out.WriteString("</" + name + ">")
			continue
		}

		if name == "a" {
			if href := sanitizedHref(attrs); href != "" {
				out.WriteString(`<a href="` + html.EscapeString(href) + `">`)
			} else {
				out.WriteString("<a>")
			}
			continue
		}

		out.WriteString("<" + name + ">")
	}
	out.WriteString(html.EscapeString(in[last:])) // trailing text after the last matched tag: same rule as above
	return out.String()
}

// parseTag splits a tag's raw "<...>" content (without the angle brackets)
// into its lowercased name, its raw attribute string, and whether it is a
// closing (</name>) or self-closing (<name/>) tag.
func parseTag(content string) (name, attrs string, closing, selfClose bool) {
	s := strings.TrimSpace(content)
	closing = strings.HasPrefix(s, "/")
	s = strings.TrimPrefix(s, "/")
	s = strings.TrimSpace(s)
	selfClose = strings.HasSuffix(s, "/")
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSpace(s)
	name, attrs, _ = strings.Cut(s, " ")
	if name == "" {
		name, attrs, _ = strings.Cut(s, "\t")
	}
	return strings.ToLower(name), attrs, closing, selfClose
}

// sanitizedHref returns attrs' href value when present and its scheme is
// http, https or mailto (allowedLinkSchemes), or "" otherwise (missing
// href, unparseable URL, or a disallowed scheme like javascript:).
func sanitizedHref(attrs string) string {
	m := hrefRe.FindStringSubmatch(attrs)
	if m == nil {
		return ""
	}
	var raw string
	for _, g := range []string{m[2], m[3], m[4]} {
		if g != "" {
			raw = g
			break
		}
	}
	if raw == "" {
		return ""
	}
	raw = html.UnescapeString(raw)
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || !allowedLinkSchemes[strings.ToLower(u.Scheme)] {
		return ""
	}
	return raw
}
