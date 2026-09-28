package brand

import (
	"bytes"
	"fmt"
	"io/fs"
	"strings"

	"gopkg.in/yaml.v3"

	"water/internal/canon"
)

// Link is one signature link line (e.g. "renaissance.example.com" ->
// https://example.com, or the optional call-to-action button).
type Link struct {
	Label string `yaml:"label"`
	URL   string `yaml:"url"`
}

// Signature is a twin's email signature block, loaded from
// twins/<twin>/brand/signature.yaml (the CEO twin's copy lives at
// twins/ceo/brand/signature.yaml). It is the one source of truth for "who
// is signing this email" — internal/connectors/google/gmail's send path is
// expected to source Name/Title/Company/Signoff/Links/CTA from here rather
// than from config's older agent.signature_name field.
//
// CTA is a pointer specifically so an absent "cta:" key in the YAML loads
// as a nil *Link, never a zero-value Link{} — RenderEmail (email.go) uses
// that nilness to decide whether to render a call-to-action button at all,
// so a caller can never accidentally render an empty, label-less button.
type Signature struct {
	Name    string `yaml:"name"`
	Title   string `yaml:"title"`
	Company string `yaml:"company"`
	Signoff string `yaml:"signoff"`
	Links   []Link `yaml:"links"`
	CTA     *Link  `yaml:"cta"`
}

// LoadSignature reads and parses a signature.yaml file at path within fsys
// (e.g. water.TwinsFS() and "twins/ceo/brand/signature.yaml" — the same
// embedded-tree-plus-path convention internal/cli/twin.go's
// loadTwinManifest/buildTwinDepsFS use for the rest of a twin's files; an
// fs.FS is accepted rather than a bare filesystem path so callers can pass
// either the embedded tree or a plain os.DirFS in tests). It fails loudly
// (returns a non-nil error) both on malformed YAML syntax and on YAML that
// parses but violates the schema (Name, Title, Company and Signoff are all
// required non-empty; every entry in Links, and CTA if present, must have a
// non-empty Label and URL) — LoadSignature never returns a zero-value
// Signature silently on a bad file. The decoder rejects unknown top-level
// keys (yaml.Decoder.KnownFields(true)) so a typo'd field name fails at
// load time instead of being silently dropped.
func LoadSignature(fsys fs.FS, path string) (*Signature, error) {
	b, err := fs.ReadFile(fsys, path)
	if err != nil {
		return nil, fmt.Errorf("brand: reading signature %s: %w", path, err)
	}
	var sig Signature
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&sig); err != nil {
		return nil, fmt.Errorf("brand: parsing signature %s: %w", path, err)
	}
	if err := sig.Validate(); err != nil {
		return nil, fmt.Errorf("brand: invalid signature %s: %w", path, err)
	}
	return &sig, nil
}

// Validate checks the schema LoadSignature requires: Name, Title, Company
// and Signoff must all be non-empty (after trimming whitespace); every
// Links entry, and CTA if non-nil, must have a non-empty Label and URL.
func (s *Signature) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("name is required")
	}
	if strings.TrimSpace(s.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if strings.TrimSpace(s.Company) == "" {
		return fmt.Errorf("company is required")
	}
	if strings.TrimSpace(s.Signoff) == "" {
		return fmt.Errorf("signoff is required")
	}
	for i, l := range s.Links {
		if strings.TrimSpace(l.Label) == "" || strings.TrimSpace(l.URL) == "" {
			return fmt.Errorf("links[%d]: label and url are both required", i)
		}
	}
	if s.CTA != nil {
		if strings.TrimSpace(s.CTA.Label) == "" || strings.TrimSpace(s.CTA.URL) == "" {
			return fmt.Errorf("cta: label and url are both required")
		}
	}
	return nil
}

// Hash returns the hex sha256 of sig's canonical form, for the approval
// payload's signature_hash field (docs/slices/BRAND.md: "the approved
// payload hash covers ... the signature config"). Any field edit — Name,
// Title, Company, Signoff, a link, or the CTA's presence/absence/content —
// changes the result.
func (s *Signature) Hash() (string, error) {
	return canon.Hash(s)
}
