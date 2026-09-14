package persona

import (
	"bytes"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Doc is a markdown file split into YAML frontmatter and body.
type Doc struct {
	Front map[string]any
	Body  string
}

// Status reads the frontmatter `status` field ("unwritten" marks Phase 1 blanks).
func (d Doc) Status() string {
	if v, ok := d.Front["status"].(string); ok {
		return v
	}
	return ""
}

var htmlComment = regexp.MustCompile(`(?s)<!--.*?-->`)

// Prose returns the body with HTML comments removed and whitespace trimmed.
// A blank result means the file carries no persona content yet.
func (d Doc) Prose() string {
	return strings.TrimSpace(htmlComment.ReplaceAllString(d.Body, ""))
}

// IsBlank reports whether the file has no usable prose (scaffold only).
func (d Doc) IsBlank() bool { return d.Prose() == "" || d.Status() == "unwritten" }

// ParseDoc splits `---\nyaml\n---\nbody`. Files without frontmatter are all body.
func ParseDoc(b []byte) (Doc, error) {
	d := Doc{Front: map[string]any{}}
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if !bytes.HasPrefix(b, []byte("---\n")) && !bytes.HasPrefix(b, []byte("---\r\n")) {
		d.Body = string(b)
		return d, nil
	}
	rest := b[3:]
	rest = bytes.TrimLeft(rest, "\r")
	rest = rest[1:] // newline
	idx := bytes.Index(rest, []byte("\n---"))
	if idx < 0 {
		d.Body = string(b)
		return d, nil
	}
	front := rest[:idx]
	body := rest[idx+4:]
	if i := bytes.IndexByte(body, '\n'); i >= 0 {
		body = body[i+1:]
	} else {
		body = nil
	}
	if len(bytes.TrimSpace(front)) > 0 {
		if err := yaml.Unmarshal(front, &d.Front); err != nil {
			return d, err
		}
	}
	d.Body = string(body)
	return d, nil
}
