package brand

import (
	"strings"
	"testing"
	"testing/fstest"

	"water"
)

func TestLoadSignatureValid(t *testing.T) {
	fsys := fstest.MapFS{
		"signature.yaml": {Data: []byte(`
name: Alex Morgan
title: CEO
company: Example Holdings
signoff: "Best,"
links:
  - {label: "example.com", url: "https://example.com"}
cta:
  label: "Book a time to talk"
  url: "https://example.com/book"
`)},
	}
	sig, err := LoadSignature(fsys, "signature.yaml")
	if err != nil {
		t.Fatalf("LoadSignature: %v", err)
	}
	if sig.Name != "Alex Morgan" || sig.Title != "CEO" || sig.Company != "Example Holdings" || sig.Signoff != "Best," {
		t.Fatalf("fields mismatch: %+v", sig)
	}
	if len(sig.Links) != 1 || sig.Links[0].Label != "example.com" || sig.Links[0].URL != "https://example.com" {
		t.Fatalf("links mismatch: %+v", sig.Links)
	}
	if sig.CTA == nil {
		t.Fatal("CTA is nil, want present")
	}
	if sig.CTA.Label != "Book a time to talk" || sig.CTA.URL != "https://example.com/book" {
		t.Fatalf("CTA mismatch: %+v", sig.CTA)
	}
}

// TestLoadSignatureNoCTAIsNilPointer is the specific regression the brief
// calls out: an absent "cta" key must load as a nil pointer, never a
// rendered-empty-button zero value.
func TestLoadSignatureNoCTAIsNilPointer(t *testing.T) {
	fsys := fstest.MapFS{
		"signature.yaml": {Data: []byte(`
name: Alex Morgan
title: CEO
company: Example Holdings
signoff: "Best,"
links:
  - {label: "example.com", url: "https://example.com"}
`)},
	}
	sig, err := LoadSignature(fsys, "signature.yaml")
	if err != nil {
		t.Fatalf("LoadSignature: %v", err)
	}
	if sig.CTA != nil {
		t.Fatalf("CTA = %+v, want nil when the key is absent", sig.CTA)
	}
}

func TestLoadSignatureMalformedYAMLSyntax(t *testing.T) {
	fsys := fstest.MapFS{
		"signature.yaml": {Data: []byte(`
name: Alex Morgan
  title: [unterminated
`)},
	}
	if _, err := LoadSignature(fsys, "signature.yaml"); err == nil {
		t.Fatal("LoadSignature with broken YAML syntax returned nil error")
	}
}

func TestLoadSignatureSchemaViolations(t *testing.T) {
	cases := map[string]string{
		"missing name": `
title: CEO
company: Example Holdings
signoff: "Best,"
`,
		"missing title": `
name: Alex Morgan
company: Example Holdings
signoff: "Best,"
`,
		"missing company": `
name: Alex Morgan
title: CEO
signoff: "Best,"
`,
		"missing signoff": `
name: Alex Morgan
title: CEO
company: Example Holdings
`,
		"link missing url": `
name: Alex Morgan
title: CEO
company: Example Holdings
signoff: "Best,"
links:
  - {label: "example.com"}
`,
		"cta missing label": `
name: Alex Morgan
title: CEO
company: Example Holdings
signoff: "Best,"
cta: {url: "https://example.com/book"}
`,
		"unknown field": `
name: Alex Morgan
title: CEO
company: Example Holdings
signoff: "Best,"
favorite_color: blue
`,
	}
	for label, yaml := range cases {
		t.Run(label, func(t *testing.T) {
			fsys := fstest.MapFS{"signature.yaml": {Data: []byte(yaml)}}
			sig, err := LoadSignature(fsys, "signature.yaml")
			if err == nil {
				t.Fatalf("LoadSignature(%s) = %+v, nil, want an error", label, sig)
			}
		})
	}
}

// TestLoadSignatureRealCEOTwin loads the actual embedded
// twins/ceo/brand/signature.yaml (not a test fixture), through the same
// embedded-tree convention internal/cli/twin.go uses for the rest of a
// twin's files (water.TwinsFS(), path "twins/<id>/..."). This is the
// concrete guarantee that the real, non-demo CEO twin's own signature file
// loads without error through this loader.
func TestLoadSignatureRealCEOTwin(t *testing.T) {
	sig, err := LoadSignature(water.TwinsFS(), "twins/ceo/brand/signature.yaml")
	if err != nil {
		t.Fatalf("LoadSignature(real ceo twin): %v", err)
	}
	if strings.TrimSpace(sig.Name) == "" {
		t.Fatal("real ceo twin signature has empty Name")
	}
	if !strings.Contains(sig.Company, "Example") {
		t.Fatalf("real ceo twin signature.company = %q, want an example.com-style placeholder", sig.Company)
	}
}
