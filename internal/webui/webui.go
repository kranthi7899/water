// Package webui is the workspace UI (Slice V-ui, docs/slices/V.md §5): plain
// HTML, CSS and vanilla JavaScript, embedded into the water binary and served
// by the daemon under one path prefix (gateway mounts it at /ui/, behind the
// same client-token auth as every other route).
//
// The macOS app loads it in a WKWebView as water://app/ui/, through a
// WKURLSchemeHandler that proxies into the daemon's Unix socket and adds the
// bearer token itself, so the page never sees the token and there is never a
// TCP port. The page's API calls are root-relative ("/v1/today"), so they
// resolve to the same water:// origin and pass through that same handler and
// its path allowlist.
//
// Decision evidence, email bodies, envelope payloads, anchor snapshots and
// meeting recaps are attacker-controlled text. The JavaScript renders every
// server-provided string with textContent/createTextNode only; the CSP below
// forbids inline script, inline style, eval and any other origin, and asks for
// Trusted Types so a string-to-HTML sink throws even if one were introduced.
// webui_test.go fails the build if any shipped file names a forbidden API.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed static
var static embed.FS

// CSP is the Content-Security-Policy every response from Handler carries,
// assets and errors alike. Everything is same-origin only; no inline script
// or style (no 'unsafe-inline'), no eval (no 'unsafe-eval'), no plugins, no
// frames, no form posts, no <base> rewriting, and Trusted Types required for
// every DOM XSS sink.
const CSP = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; " +
	"font-src 'self'; connect-src 'self'; media-src 'none'; object-src 'none'; frame-src 'none'; " +
	"worker-src 'none'; manifest-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; " +
	"require-trusted-types-for 'script'; trusted-types 'none'"

// MetaCSP is CSP as index.html repeats it in a <meta http-equiv> tag, as a
// second line of defence should a proxy ever drop the header. A meta policy
// cannot carry frame-ancestors (browsers ignore it there), so it is left out;
// webui_test.go checks the two stay in step.
var MetaCSP = strings.Replace(CSP, " frame-ancestors 'none';", "", 1)

// contentTypes maps the only file extensions the UI ships to their media
// types. A file with any other extension is never served.
var contentTypes = map[string]string{
	".html": "text/html; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".js":   "text/javascript; charset=utf-8",
}

// Files lists every embedded asset, relative to the UI root (e.g.
// "index.html", "app.js").
func Files() ([]string, error) {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		return nil, err
	}
	var out []string
	err = fs.WalkDir(sub, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

// ReadFile returns one embedded asset by its Files name.
func ReadFile(name string) ([]byte, error) {
	return static.ReadFile("static/" + name)
}

// SetSecurityHeaders sets the headers every UI response carries.
func SetSecurityHeaders(h http.Header) {
	h.Set("Content-Security-Policy", CSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Cache-Control", "no-cache")
}

// Handler serves the embedded assets under prefix (which must end in "/").
// prefix itself serves index.html. Only GET/HEAD reach it (the gateway's
// route pattern is method-scoped). There is no directory listing, no
// filesystem access beyond the embedded tree, and every response, 404s
// included, carries the security headers.
func Handler(prefix string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		SetSecurityHeaders(w.Header())
		rest, ok := strings.CutPrefix(r.URL.Path, prefix)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if rest == "" {
			rest = "index.html"
		}
		// Reject anything that isn't already a clean relative path:
		// "..", "./", "//" and the like never name an asset.
		if path.Clean(rest) != rest || strings.HasPrefix(rest, "/") || strings.Contains(rest, "..") {
			http.NotFound(w, r)
			return
		}
		ct, ok := contentTypes[path.Ext(rest)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		b, err := ReadFile(rest)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", ct)
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			_, _ = w.Write(b)
		}
	})
}
