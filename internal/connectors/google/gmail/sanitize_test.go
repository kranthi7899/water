package gmail

import (
	"strings"
	"testing"
)

// TestSanitizeBodyStripsScript is one of docs/slices/BRAND.md task 7's
// exact named cases: a <script> element -- tag and content both -- must not
// survive, while legitimate allowlisted content around it does.
func TestSanitizeBodyStripsScript(t *testing.T) {
	got := sanitizeBody(`<p>Hello</p><script>alert(document.cookie)</script><p>World</p>`)
	if strings.Contains(got, "<script") || strings.Contains(got, "alert(") {
		t.Fatalf("script survived sanitizing: %q", got)
	}
	if !strings.Contains(got, "<p>Hello</p>") || !strings.Contains(got, "<p>World</p>") {
		t.Fatalf("legitimate content lost: %q", got)
	}
}

// TestSanitizeBodyStripsImg is the brief's <img> case: no img tag or
// attribute (including an event-handler attribute) survives.
func TestSanitizeBodyStripsImg(t *testing.T) {
	got := sanitizeBody(`<p>Look</p><img src="http://evil.example/x.png" onerror="alert(1)"><p>at this</p>`)
	if strings.Contains(got, "<img") || strings.Contains(got, "onerror") {
		t.Fatalf("img survived sanitizing: %q", got)
	}
	if !strings.Contains(got, "<p>Look</p>") || !strings.Contains(got, "<p>at this</p>") {
		t.Fatalf("legitimate content lost: %q", got)
	}
}

// TestSanitizeBodyStripsJavascriptLink is the brief's javascript: link
// case: links are restricted to http/https/mailto, so the href is dropped
// (the anchor and its text survive as plain, non-linked text).
func TestSanitizeBodyStripsJavascriptLink(t *testing.T) {
	got := sanitizeBody(`<p><a href="javascript:alert(document.cookie)">Click here</a></p>`)
	if strings.Contains(got, "javascript:") {
		t.Fatalf("javascript: scheme survived sanitizing: %q", got)
	}
	if !strings.Contains(got, "Click here") {
		t.Fatalf("link text lost: %q", got)
	}
}

// TestSanitizeBodyStripsInlineStyle is the brief's inline style="" case: no
// allowed tag keeps a style attribute (or any attribute besides <a>'s own
// validated href).
func TestSanitizeBodyStripsInlineStyle(t *testing.T) {
	got := sanitizeBody(`<p style="color:red;position:fixed">Hi</p>`)
	if strings.Contains(got, "style") {
		t.Fatalf("style attribute survived sanitizing: %q", got)
	}
	if !strings.Contains(got, "Hi") {
		t.Fatalf("text lost: %q", got)
	}
	if got != "<p>Hi</p>" {
		t.Fatalf("got %q, want exactly <p>Hi</p>", got)
	}
}

// TestSanitizeBodyKeepsAllowlistedMarkup confirms the allowlist itself (p,
// br, a, strong, em, ul, ol, li) survives sanitizing intact, including a
// legitimate http(s)/mailto link with its href kept.
func TestSanitizeBodyKeepsAllowlistedMarkup(t *testing.T) {
	in := `<p>See <a href="https://example.com">this</a> and <strong>bold</strong>, <em>em</em>.</p>` +
		`<ul><li>one</li><li>two</li></ul>` +
		`<p>Reach me at <a href="mailto:dana@acme.com">dana@acme.com</a>.</p>` +
		`Line1<br>Line2`
	got := sanitizeBody(in)
	for _, want := range []string{
		`<a href="https://example.com">this</a>`,
		"<strong>bold</strong>", "<em>em</em>",
		"<li>one</li>", "<li>two</li>",
		`<a href="mailto:dana@acme.com">dana@acme.com</a>`,
		"<br>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("sanitized body missing %q: got %q", want, got)
		}
	}
}

// TestSanitizeBodyDropsDisallowedTagButKeepsText covers an unknown tag
// (neither allowlisted nor script/style/img): the tag itself is dropped,
// its text content is kept.
func TestSanitizeBodyDropsDisallowedTagButKeepsText(t *testing.T) {
	got := sanitizeBody(`<div class="card"><p>Hello</p></div>`)
	if strings.Contains(got, "<div") || strings.Contains(got, "class=") {
		t.Fatalf("disallowed tag/attribute survived: %q", got)
	}
	if !strings.Contains(got, "<p>Hello</p>") {
		t.Fatalf("legitimate content lost: %q", got)
	}
}

// TestSanitizeBodyPlainTextBecomesParagraphs covers the common case (the
// write schema's own "plain-text body"): a body with no markup at all is
// turned into <p>/<br> so it doesn't collapse into one run-on line once it
// reaches an HTML mail client.
func TestSanitizeBodyPlainTextBecomesParagraphs(t *testing.T) {
	got := sanitizeBody("First paragraph,\nstill first line two.\n\nSecond paragraph.")
	want := "<p>First paragraph,<br>still first line two.</p><p>Second paragraph.</p>"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestSanitizeBodyPlainTextEscapesAmpersandsAndAngleBrackets covers a
// plain-text body that happens to contain literal '&'/'<'/'>' characters
// (not markup): they must come out HTML-escaped, not interpreted as tags.
func TestSanitizeBodyPlainTextEscapesAmpersandsAndAngleBrackets(t *testing.T) {
	got := sanitizeBody("Q3 revenue was up & margins improved.")
	want := "<p>Q3 revenue was up &amp; margins improved.</p>"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestSanitizeBodySameSanitizedBodyDrivesBothRenderings covers task 7's
// "one sanitize pass, two renderings" requirement at the level this
// package can test it: sanitizeBody's output, not the raw input, is what
// both RenderEmail outputs must be built from -- i.e. sanitizing is
// idempotent, so calling it once (as renderSendBody does) and once more (as
// a second, differently-configured pass might) never disagree.
func TestSanitizeBodySameSanitizedBodyDrivesBothRenderings(t *testing.T) {
	in := `<p>Hello <script>evil()</script><strong>world</strong></p>`
	once := sanitizeBody(in)
	twice := sanitizeBody(once)
	if once != twice {
		t.Fatalf("sanitizeBody is not idempotent: once=%q twice=%q", once, twice)
	}
}

// TestSanitizeBodyEscapesLeakedTagFragments is a real bypass found and fixed
// during review (2026-09-28), not a hypothetical: tagRe's `[^<>]*` cannot
// span an embedded '<', so a '<' meant to start one tag can fail to match at
// that position, and the scan instead matches a later, incidental "<...>"
// span elsewhere in the input -- everything in between becomes "text",
// including fragments of what was meant to be a single dangerous tag. Before
// the fix, that leaked text was written raw and could reassemble into a
// working attribute injection that never passed through sanitizedHref's
// scheme check at all -- this exact input produced a surviving
// `onclick="alert(1)"` on the <a> tag.
func TestSanitizeBodyEscapesLeakedTagFragments(t *testing.T) {
	in := `click <a href="ht<script>x</script>tp://evil" onclick="alert(1)">here</a>`
	got := sanitizeBody(in)
	if strings.Contains(got, "<script") {
		t.Fatalf("script tag survived: %q", got)
	}
	// The danger was a LIVE, executable onclick= attribute on a real <a>
	// tag. Once escaped, "onclick=" surviving as inert text is fine (a
	// mail client renders &#34; as a literal quote character, never as
	// markup) -- what must never survive is a raw, un-escaped '<a ...>'
	// that a downstream HTML parser would treat as a real element.
	if strings.Contains(got, `<a href="http://evil"`) || strings.Contains(got, "<a onclick") {
		t.Fatalf("leaked fragments reassembled into a live, executable tag: %q", got)
	}
	if !strings.Contains(got, "&lt;a href=") {
		t.Fatalf("leaked tag fragment was not HTML-escaped: %q", got)
	}
}

// TestSanitizeBodyEscapesUnclosedTagLeak covers the simpler case of the
// same bypass class: an intentionally-unclosed tag whose leaked prefix must
// come out as inert text, not a raw '<' a mail client's own HTML parser
// could treat as the start of a real tag.
func TestSanitizeBodyEscapesUnclosedTagLeak(t *testing.T) {
	got := sanitizeBody(`<scr<script>ipt>alert(1)</script>`)
	if strings.Contains(got, "alert(1)") {
		t.Fatalf("script payload survived: %q", got)
	}
	if strings.Contains(got, "<scr") && !strings.Contains(got, "&lt;scr") {
		t.Fatalf("leaked '<' written raw instead of escaped: %q", got)
	}
}
