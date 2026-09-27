package webui

import (
	"net/http"
	"net/http/httptest"
	"path"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// forbidden is every construct that turns a string into markup or code, or
// reaches another origin. Server-provided text in this UI is attacker
// controlled (mail bodies, decision evidence, meeting recaps), so none of
// these may appear anywhere in a shipped file, not even in a comment: the
// scan is deliberately dumb and literal, a tripwire, with the CSP's Trusted
// Types requirement as the runtime backstop.
var forbidden = []struct {
	name string
	re   *regexp.Regexp
}{
	{"innerHTML", regexp.MustCompile(`innerHTML`)},
	{"outerHTML", regexp.MustCompile(`outerHTML`)},
	{"insertAdjacentHTML", regexp.MustCompile(`insertAdjacentHTML`)},
	{"setHTMLUnsafe", regexp.MustCompile(`setHTMLUnsafe|parseHTMLUnsafe`)},
	{"document.write", regexp.MustCompile(`document\s*\.\s*write`)},
	{"eval", regexp.MustCompile(`\beval\b`)},
	{"Function constructor", regexp.MustCompile(`\bFunction\s*\(|new\s+Function\b`)},
	{"string setTimeout/setInterval", regexp.MustCompile("set(Timeout|Interval)\\s*\\(\\s*['\"`]")},
	{"javascript: URL", regexp.MustCompile(`(?i)javascript\s*:`)},
	{"data: URL", regexp.MustCompile(`(?i)\bdata\s*:\s*[a-z]+/`)},
	{"createContextualFragment", regexp.MustCompile(`createContextualFragment`)},
	{"DOMParser", regexp.MustCompile(`DOMParser|parseFromString`)},
	{"srcdoc", regexp.MustCompile(`(?i)srcdoc`)},
	{"dynamic import", regexp.MustCompile(`\bimport\s*\(|importScripts`)},
	{"window.open", regexp.MustCompile(`window\s*\.\s*open\b`)},
	{"URL-bearing property write", regexp.MustCompile(`\.(href|src|action|formAction)\s*=[^=]`)},
	{"location write", regexp.MustCompile(`location\s*=[^=]|location\.(href|assign|replace)\b`)},
	{"unsafe setAttribute", regexp.MustCompile(`(?i)setAttribute(NS)?\s*\(\s*['"\x60]?\s*(on|href|src|style|srcdoc|action|formaction|xlink)`)},
	{"external URL", regexp.MustCompile(`(?i)(https?|wss?|ftp)://|["'(]\s*//[a-z0-9]`)},
	{"CSS @import", regexp.MustCompile(`@import`)},
	{"CSS url()", regexp.MustCompile(`(?i)url\s*\(`)},
	{"CSS expression()", regexp.MustCompile(`(?i)expression\s*\(`)},
}

// htmlOnly are extra rules for .html files: no inline script bodies, no
// inline styles, no inline event handlers (the CSP would block all three,
// but a blocked-at-runtime feature is still a bug).
var htmlOnly = []struct {
	name string
	re   *regexp.Regexp
}{
	{"inline <script> body", regexp.MustCompile(`(?is)<script\b[^>]*>\s*[^<\s]`)},
	{"<style> block", regexp.MustCompile(`(?i)<style\b`)},
	{"style attribute", regexp.MustCompile(`(?i)\sstyle\s*=`)},
	{"inline event handler", regexp.MustCompile(`(?i)\son[a-z]+\s*=`)},
	{"<iframe>/<object>/<embed>/<base>", regexp.MustCompile(`(?i)<(iframe|object|embed|base|frame)\b`)},
}

// violations reports every forbidden construct in one file's content.
func violations(name string, b []byte) []string {
	var out []string
	src := string(b)
	check := func(rule string, re *regexp.Regexp) {
		for _, loc := range re.FindAllStringIndex(src, -1) {
			line := strings.Count(src[:loc[0]], "\n") + 1
			out = append(out, name+":"+strconv.Itoa(line)+": "+rule+": "+strings.TrimSpace(src[loc[0]:loc[1]]))
		}
	}
	for _, f := range forbidden {
		check(f.name, f.re)
	}
	if path.Ext(name) == ".html" {
		for _, f := range htmlOnly {
			check(f.name, f.re)
		}
		for _, loc := range scriptTag.FindAllStringIndex(src, -1) {
			if !scriptSrc.MatchString(src[loc[0]:loc[1]]) {
				line := strings.Count(src[:loc[0]], "\n") + 1
				out = append(out, name+":"+strconv.Itoa(line)+": <script> without src: "+src[loc[0]:loc[1]])
			}
		}
	}
	return out
}

var (
	scriptTag = regexp.MustCompile(`(?i)<script\b[^>]*>`)
	scriptSrc = regexp.MustCompile(`(?i)\ssrc\s*=\s*"[a-z0-9_-]+\.js"`)
)

// TestShippedFilesUseNoUnsafeAPIs scans every embedded file for the
// forbidden constructs. A hit fails the build.
func TestShippedFilesUseNoUnsafeAPIs(t *testing.T) {
	files, err := Files()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no embedded UI files")
	}
	for _, f := range files {
		if _, ok := contentTypes[path.Ext(f)]; !ok {
			t.Errorf("%s: unexpected file type shipped in the UI (only .html, .css, .js)", f)
		}
		b, err := ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range violations(f, b) {
			t.Error(v)
		}
	}
}

// TestScannerCatchesEachForbiddenConstruct proves the scan above is not
// vacuous: each sample must be flagged.
func TestScannerCatchesEachForbiddenConstruct(t *testing.T) {
	bad := map[string]string{
		"a.js":   "el.innerHTML = x",
		"b.js":   "el.outerHTML = x",
		"c.js":   "el.insertAdjacentHTML('beforeend', x)",
		"d.js":   "document.write(x)",
		"e.js":   "eval(x)",
		"f.js":   "new Function('return 1')",
		"g.js":   "setTimeout('alert(1)', 5)",
		"h.js":   "setInterval(\"x()\", 5)",
		"i.js":   "a.href = 'JavaScript:alert(1)'",
		"j.js":   "fetch('https://evil.example/')",
		"k.js":   "el.setAttribute('onclick', x)",
		"l.js":   "el.setAttribute(\"href\", x)",
		"m.js":   "range.createContextualFragment(x)",
		"n.js":   "new DOMParser().parseFromString(x, 't')",
		"o.js":   "location.href = x",
		"p.js":   "img.src = x",
		"q.js":   "import('x')",
		"r.js":   "window.open(x)",
		"s.css":  "@import 'x.css';",
		"t.css":  "body { background: url(x.png) }",
		"u.js":   "const f = Function('x')",
		"v.js":   "fetch('//evil.example/x')",
		"a.html": `<script>alert(1)</script>`,
		"b.html": `<script defer></script>`,
		"c.html": `<div style="color:red"></div>`,
		"d.html": `<button onclick="x()"></button>`,
		"e.html": `<iframe></iframe>`,
		"f.html": `<style>body{}</style>`,
	}
	for name, src := range bad {
		if len(violations(name, []byte(src))) == 0 {
			t.Errorf("%s: %q was not flagged", name, src)
		}
	}
	good := map[string]string{
		"a.js":   "el.textContent = x; el.appendChild(document.createTextNode(y)); setTimeout(refresh, 5000); if (a == b) {}",
		"b.html": `<script src="app.js" defer></script><link rel="stylesheet" href="app.css">`,
	}
	for name, src := range good {
		if v := violations(name, []byte(src)); len(v) != 0 {
			t.Errorf("%s: false positive: %v", name, v)
		}
	}
}

// TestEveryAssetResponseCarriesTheCSP serves every embedded file (and a
// miss) through Handler and checks the security headers and content type.
func TestEveryAssetResponseCarriesTheCSP(t *testing.T) {
	srv := httptest.NewServer(Handler("/ui/"))
	t.Cleanup(srv.Close)
	files, err := Files()
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"/ui/"}
	for _, f := range files {
		paths = append(paths, "/ui/"+f)
	}
	for _, p := range paths {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d, want 200", p, resp.StatusCode)
		}
		checkSecurityHeaders(t, p, resp.Header)
		want := contentTypes[path.Ext(p)]
		if p == "/ui/" {
			want = contentTypes[".html"]
		}
		if got := resp.Header.Get("Content-Type"); got != want {
			t.Errorf("%s: Content-Type %q, want %q", p, got, want)
		}
	}
	for _, miss := range []string{"/ui/nope.js", "/ui/../webui.go", "/ui//index.html", "/ui/./app.js", "/ui/static/index.html", "/ui/webui.go"} {
		resp, err := http.Get(srv.URL + miss)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("%s: status 200, want a miss", miss)
		}
		checkSecurityHeaders(t, miss, resp.Header)
	}
}

func checkSecurityHeaders(t *testing.T, p string, h http.Header) {
	t.Helper()
	if got := h.Get("Content-Security-Policy"); got != CSP {
		t.Errorf("%s: Content-Security-Policy %q, want %q", p, got, CSP)
	}
	if got := h.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("%s: X-Content-Type-Options %q, want nosniff", p, got)
	}
}

// TestCSPIsStrict pins the policy's non-negotiable parts, so a later edit
// can't quietly loosen it.
func TestCSPIsStrict(t *testing.T) {
	for _, bad := range []string{"unsafe-inline", "unsafe-eval", "unsafe-hashes", "wasm-unsafe-eval", "*", "http:", "https:", "data:", "blob:"} {
		if strings.Contains(CSP, bad) {
			t.Errorf("CSP contains %q: %s", bad, CSP)
		}
	}
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "style-src 'self'", "img-src 'self'", "connect-src 'self'", "object-src 'none'", "base-uri 'none'", "frame-ancestors 'none'", "require-trusted-types-for 'script'"} {
		if !strings.Contains(CSP, want) {
			t.Errorf("CSP lacks %q: %s", want, CSP)
		}
	}
}

// TestNoRawPills is docs/slices/UI.md Phase 0b's "no raw pills" acceptance
// item: no shipped .js file may contain the literal old pill strings once
// used to render a raw "Severity N" badge, an "External content" badge, a
// readiness badge shown even when ready, or a raw "Origin p0" label. Phase
// 0b replaced these with plain-language text, a titled glyph, and a
// priority-based row/card border class instead.
//
// Phase 2 split the code that used to render these (Today, the decision
// card) out of app.js into view_today.js/view_decisions.js, so this test
// scans every shipped .js file (webui.Files(), not a single hardcoded
// name) — otherwise it would trivially "pass" on an app.js that no longer
// contains the relevant code at all, rather than actually proving the
// pills are gone from wherever that code now lives.
func TestNoRawPills(t *testing.T) {
	files, err := Files()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if path.Ext(f) != ".js" {
			continue
		}
		b, err := ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, pill := range []string{"'Severity '", "'External content'", "ready: 'Ready'", "'Origin '"} {
			if strings.Contains(src, pill) {
				t.Errorf("%s still contains the raw pill %s", f, pill)
			}
		}
	}
}

// TestIndexMetaCSPMatchesTheHeader keeps index.html's <meta> copy of the
// policy in step with the header.
func TestIndexMetaCSPMatchesTheHeader(t *testing.T) {
	b, err := ReadFile("index.html")
	if err != nil {
		t.Fatal(err)
	}
	want := `<meta http-equiv="Content-Security-Policy" content="` + MetaCSP + `">`
	if !strings.Contains(string(b), want) {
		t.Errorf("index.html lacks %s", want)
	}
}
