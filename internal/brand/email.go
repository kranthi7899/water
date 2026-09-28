package brand

import (
	"bytes"
	"embed"
	"html"
	htmltemplate "html/template"
	"regexp"
	"strings"
	texttemplate "text/template"
)

//go:embed templates/email-template.html templates/email-template.txt
var templateFS embed.FS

var htmlTmpl = htmltemplate.Must(htmltemplate.New("email-template.html").
	ParseFS(templateFS, "templates/email-template.html"))

var textTmpl = texttemplate.Must(texttemplate.New("email-template.txt").
	ParseFS(templateFS, "templates/email-template.txt"))

// htmlView is the html/template data for email-template.html. Body is
// html/template.HTML (not a plain string) specifically because it already
// holds sanitized HTML written by a later agent's sanitizer
// (docs/slices/BRAND.md task A5) — html/template would otherwise re-escape
// it into visible tags on the page.
type htmlView struct {
	Body      htmltemplate.HTML
	Signature Signature
}

// textView is the text/template data for email-template.txt. text/template
// never escapes, so Body here is a plain string: the html body converted to
// plain text by htmlToText.
type textView struct {
	Body      string
	Signature Signature
}

// RenderEmail renders both the HTML and plain-text parts of a Water email
// from body (sanitized HTML written by the model — see internal/brand's
// package doc) and sig (a loaded Signature, typically from
// twins/<twin>/brand/signature.yaml via LoadSignature). It is the one
// function the gmail send path calls to turn a drafted body into the two
// MIME alternative parts; the caller is responsible for embedding the three
// cid: images (AssetBytes/AssetHash) and for building the actual MIME
// message around this output.
//
// Both outputs carry the same content: the body, a signature block
// (Name/Title/Company/Signoff/Links, plus a CTA link only when sig.CTA is
// non-nil), and the disclosure line "Sent by Water, an AI assistant, on
// behalf of <name>." exactly once. The HTML output additionally references
// cid:water-header, cid:water-koi and cid:water-glass; the text output has
// no images at all.
func RenderEmail(body string, sig Signature) (htmlOut, textOut string, err error) {
	var hbuf bytes.Buffer
	if err := htmlTmpl.Execute(&hbuf, htmlView{
		Body:      htmltemplate.HTML(body), //nolint:gosec // body is caller-sanitized HTML, by contract (see doc comment)
		Signature: sig,
	}); err != nil {
		return "", "", err
	}

	var tbuf bytes.Buffer
	if err := textTmpl.Execute(&tbuf, textView{
		Body:      htmlToText(body),
		Signature: sig,
	}); err != nil {
		return "", "", err
	}

	return hbuf.String(), tbuf.String(), nil
}

var (
	reBreak     = regexp.MustCompile(`(?i)<br\s*/?>`)
	reParaClose = regexp.MustCompile(`(?i)</p\s*>`)
	reParaOpen  = regexp.MustCompile(`(?i)<p[^>]*>`)
	reListItem  = regexp.MustCompile(`(?i)<li[^>]*>`)
	reListClose = regexp.MustCompile(`(?i)</li\s*>`)
	reLink      = regexp.MustCompile(`(?is)<a\s[^>]*href="([^"]*)"[^>]*>(.*?)</a>`)
	reAnyTag    = regexp.MustCompile(`<[^>]*>`)
	reBlankRuns = regexp.MustCompile(`\n{3,}`)
)

// htmlToText converts sanitized HTML (the allowlist is p, br, a, strong, em,
// ul, ol, li — see docs/slices/BRAND.md task A5) into the equivalent plain
// text for email-template.txt: paragraphs and <br> become newlines, list
// items become "- " lines, links become "text (url)", and every other tag
// (including strong/em, which plain text can't represent, and ul/ol, which
// are structural only) is dropped, keeping its inner text. This is a
// best-effort renderer for the sanitizer's own allowlist, not a general
// HTML-to-text library — it is not expected to handle arbitrary HTML.
func htmlToText(in string) string {
	s := in
	s = reLink.ReplaceAllString(s, "$2 ($1)")
	s = reListItem.ReplaceAllString(s, "\n- ")
	s = reListClose.ReplaceAllString(s, "")
	s = reBreak.ReplaceAllString(s, "\n")
	s = reParaClose.ReplaceAllString(s, "\n\n")
	s = reParaOpen.ReplaceAllString(s, "")
	s = reAnyTag.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = reBlankRuns.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}
