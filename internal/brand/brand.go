// Package brand is the one source of truth for Water's visual identity:
// the palette and type scale from docs/design/brand/documents.md §2-3, the
// three embedded image assets the email template references as cid: URLs,
// the CEO twin's signature block (twins/ceo/brand/signature.yaml), and the
// email templates themselves (RenderEmail, in email.go).
//
// Nothing outside this package should hold a second copy of the palette hex
// values, the embedded assets, or the email template source — every other
// package (the gmail send path, the approval card, later the document
// renderer) is expected to call into here instead.
//
// docs/design/brand/ (outside the Go module's build) is this package's
// design source; internal/brand/assets/ holds the actual bytes go:embed
// ships, copied from there by hand. Re-copy and rebuild whenever the design
// source changes (see CLAUDE.md's "Build and embed" rule).
package brand

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"strings"
)

// TemplateVersion identifies the email template's content — the exact
// wording and layout of templates/email-template.{html,txt} and the fixed
// part of the signature/disclosure markup around {{.Body}}. It is bumped by
// hand (a plain string, e.g. "v2") whenever those template files change in
// any way that alters what a recipient sees, never automatically. Callers
// that build an approval envelope for a sent email (docs/slices/BRAND.md's
// payload-hash extension) include this value in the payload alongside the
// signature hash and asset hashes, so editing the template after an email
// was approved but before it sends invalidates that approval.
const TemplateVersion = "v1"

//go:embed assets/water-header.png assets/water-koi-288.png assets/glass-band-600.png
var assets embed.FS

// asset names, as passed to AssetHash and referenced by the email template
// as cid:<name> (see email.go).
const (
	AssetHeader = "water-header"
	AssetKoi    = "water-koi"
	AssetGlass  = "water-glass"
)

// assetFiles maps an asset name (AssetHeader etc.) to its embedded path.
var assetFiles = map[string]string{
	AssetHeader: "assets/water-header.png",
	AssetKoi:    "assets/water-koi-288.png",
	AssetGlass:  "assets/glass-band-600.png",
}

// AssetBytes returns the raw bytes of the embedded asset named name (one of
// AssetHeader, AssetKoi, AssetGlass). It errors for an unknown name.
func AssetBytes(name string) ([]byte, error) {
	path, ok := assetFiles[name]
	if !ok {
		return nil, fmt.Errorf("brand: unknown asset %q", name)
	}
	return assets.ReadFile(path)
}

// AssetHash returns the hex-encoded sha256 of the embedded asset named name
// (one of AssetHeader, AssetKoi, AssetGlass). The value is stable for the
// life of a build — it changes only when the embedded bytes change — which
// is what makes it useful as one of the approval payload's integrity
// fields (docs/slices/BRAND.md: "the approved payload hash covers ... the
// asset hashes"). It errors for an unknown name; there is no zero-value
// fallback, since a silently empty hash would defeat the point of covering
// the asset in the payload hash at all.
func AssetHash(name string) (string, error) {
	b, err := AssetBytes(name)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// CombinedAssetHash returns one hex-encoded sha256 covering
// AssetHeader/AssetKoi/AssetGlass together, in that fixed order (name then
// hash, newline-joined, so two different assets can never collide into the
// same combined value the way plain concatenation could). This is a single
// string rather than AssetHash's own per-asset result specifically because
// it goes into gmail.send_message's approval payload
// (internal/gateway/brand_payload.go), which -- once claimed -- must
// re-hash to exactly the envelope's stored PayloadHash
// (internal/approvals/queue.go's Claim); that payload reaches
// connectors.Schema.Validate as real function arguments, and
// connectors.Property has no "object" type to declare a
// name-to-hash map as (internal/connectors/schema.go) -- found live,
// 2026-09-28, see gmail.go's sendMessageSchema doc comment for the full
// story.
func CombinedAssetHash() (string, error) {
	var buf strings.Builder
	for _, name := range []string{AssetHeader, AssetKoi, AssetGlass} {
		h, err := AssetHash(name)
		if err != nil {
			return "", err
		}
		buf.WriteString(name)
		buf.WriteByte('=')
		buf.WriteString(h)
		buf.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(buf.String()))
	return hex.EncodeToString(sum[:]), nil
}

// Token is one named color in the palette (documents.md §2).
type Token struct {
	Name string // e.g. "ink" — also the CSS custom property name (--ink)
	Hex  string // e.g. "#0B0B14"
	Role string // documents.md's one-line description of where it's used
}

// Tokens is the palette, in documents.md §2's order. This is the one place
// these twelve hex values are written; every other package that needs a
// color (the email template, later the document renderer) reads it from
// here rather than hard-coding its own copy.
var Tokens = []Token{
	{"ink", "#0B0B14", "Cover, dark callouts, audit/trace in diagrams"},
	{"paper", "#F6F7FA", "Page tint for tiles and light callouts"},
	{"white", "#FFFFFF", "Body pages"},
	{"blue-sky", "#6CCBFF", "Section numbers, top band of the wordmark"},
	{"blue", "#3B94F2", "Primary accent: rules, links, tile edges, primary nodes"},
	{"blue-deep", "#2D5FB8", "Kickers, secondary emphasis"},
	{"navy", "#173561", "Table heads, sub-headings, shared/system nodes"},
	{"teal", "#1BA79A", "Data and memory; success states"},
	{"violet", "#6D5BD0", "Agents, roles, identity"},
	{"amber", "#E0A13A", "Attention, warnings, \"watch\" callouts"},
	{"coral", "#E2634E", "Errors, failures, blocked"},
	{"slate", "#6B7280", "Structure, neutral nodes, captions"},
}

// Hex looks up a token's hex value by name (e.g. "ink"). It returns "" and
// false when name isn't a known token.
func Hex(name string) (string, bool) {
	for _, t := range Tokens {
		if t.Name == name {
			return t.Hex, true
		}
	}
	return "", false
}

// CSSCustomProperties renders Tokens as CSS custom-property declarations
// (one "--name: #hex;" per line, in Tokens' order, no wrapping selector),
// suitable for splicing inside a ":root { ... }" block.
func CSSCustomProperties() string {
	var out string
	for _, t := range Tokens {
		out += fmt.Sprintf("--%s: %s;\n", t.Name, t.Hex)
	}
	return out
}

// TypeScale is one row of documents.md §3's type table.
type TypeScale struct {
	Use  string // e.g. "Titles, section names, tile numbers, kickers"
	Face string // e.g. "Archivo Expanded ExtraBold (800)"
	Size string // e.g. "Title 30pt · section 18pt · tile 20pt"
}

// TypeScales is documents.md §3, in its own row order.
var TypeScales = []TypeScale{
	{"Titles, section names, tile numbers, kickers", "Archivo Expanded ExtraBold (800)", "Title 30pt · section 18pt · tile 20pt"},
	{"Sub-headings, table heads, small labels", "Archivo Expanded SemiBold (600)", "10.5pt / 8pt with 0.08–0.2em tracking"},
	{"Body, captions, tables", "System sans (Helvetica Neue, Arial)", "Body 10.5pt/1.55 · table 9.2pt · caption 8.5pt"},
	{"Code, ids, commands", "System mono", "9pt on a pale blue chip"},
}
