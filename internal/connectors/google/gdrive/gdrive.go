// Package gdrive is the read-only Google Drive connector: search_files and
// read_file. Docs, Sheets and Slides content is read through Drive's
// files.export, so no extra scopes beyond drive.readonly are needed.
package gdrive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"water/internal/connectors"
	"water/internal/connectors/google/gapi"
	"water/internal/gate/permit"
	"water/internal/store"
	"water/internal/twins"
)

// maxContentBytes caps how much file content read_file returns.
const maxContentBytes = 50 << 10

// maxPages caps how many pages search_files will follow, in case Drive keeps
// handing back a nextPageToken without ever reaching max.
const maxPages = 10

// Drive is the "gdrive" connector. opts is nil in production; tests set it
// to point at an httptest server.
type Drive struct {
	opts *gapi.Options
}

// New builds the production connector.
func New() *Drive { return &Drive{} }

// NewWithOptions builds a connector against a test double.
func NewWithOptions(o *gapi.Options) *Drive { return &Drive{opts: o} }

func (*Drive) Name() string { return "gdrive" }

func (*Drive) Credential() (string, string) { return gapi.Service, gapi.DefaultAccount }

func (*Drive) Functions() []connectors.Function {
	return []connectors.Function{
		{
			Name:        "search_files",
			Description: "Search Drive files by keyword, or a raw Drive query (e.g. \"mimeType = 'application/vnd.google-apps.document'\").",
			Level:       twins.R,
			Risk:        connectors.RiskLow,
			External:    true,
			Schema: connectors.Schema{
				Properties: map[string]connectors.Property{
					"query": {Type: "string", Description: "keywords, or a raw Drive query string"},
					"max":   {Type: "integer", Description: "maximum results (default 20, capped at 100)"},
				},
			},
		},
		{
			Name:        "read_file",
			Description: "Read a Drive file's content (Docs/Sheets/Slides are exported, plain text and markdown are fetched directly) or its metadata.",
			Level:       twins.R,
			Risk:        connectors.RiskLow,
			External:    true,
			Schema: connectors.Schema{
				Properties: map[string]connectors.Property{"id": {Type: "string", Description: "Drive file id"}},
				Required:   []string{"id"},
			},
		},
	}
}

func (c *Drive) Invoke(ctx context.Context, p permit.Permit) (json.RawMessage, error) {
	v, err := p.Open()
	if err != nil {
		return nil, err
	}
	cl, err := gapi.FromSecret(v.Credential, c.opts)
	if err != nil {
		return nil, err
	}
	switch v.Function {
	case "search_files":
		return c.searchFiles(ctx, cl, v.Args)
	case "read_file":
		return c.readFile(ctx, cl, v.Args)
	}
	return nil, fmt.Errorf("gdrive: unknown function %q", v.Function)
}

// driveFieldRe recognizes a raw Drive query: one of its field names next to
// a comparison operator, or a "in parents"/"in owners" clause. Anything else
// is treated as plain keywords and escaped into a fullText/name search.
var driveFieldRe = regexp.MustCompile(`(?i)\b(fullText|name|mimeType|modifiedTime|viewedByMeTime|trashed|starred|owners|writers|readers|parents|properties|appProperties|visibility|sharedWithMe)\s*(contains|=|!=|>=|<=|>|<)|\bin\s+(parents|owners|writers|readers)\b`)

// driveQuery turns a search_files "query" argument into a Drive q string.
// Plain words are escaped into a fullText/name search; a string that already
// looks like Drive query syntax is passed through as its own clause.
func driveQuery(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "trashed = false"
	}
	if driveFieldRe.MatchString(raw) {
		return "trashed = false and (" + raw + ")"
	}
	esc := escapeQuoted(raw)
	return fmt.Sprintf("trashed = false and (fullText contains '%s' or name contains '%s')", esc, esc)
}

// escapeQuoted escapes a value for embedding inside a single-quoted Drive
// query string literal: backslashes first, then quotes.
func escapeQuoted(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return s
}

func (c *Drive) searchFiles(ctx context.Context, cl *gapi.Client, args map[string]any) (json.RawMessage, error) {
	max, err := gapi.ArgInt(args, "max", 20, 1, 100)
	if err != nil {
		return nil, err
	}
	q := driveQuery(gapi.ArgString(args, "query"))
	var files []json.RawMessage
	pageToken := ""
	for page := 0; page < maxPages && len(files) < max; page++ {
		var resp struct {
			Files         []json.RawMessage `json:"files"`
			NextPageToken string            `json:"nextPageToken"`
		}
		query := url.Values{
			"q":        {q},
			"fields":   {"nextPageToken,files(id,name,mimeType,owners,modifiedTime,webViewLink,trashed)"},
			"pageSize": {strconv.Itoa(min(max-len(files), 100))},
		}
		if pageToken != "" {
			query.Set("pageToken", pageToken)
		}
		if err := cl.GetJSON(ctx, gapi.DriveBase+"/files", query, &resp); err != nil {
			return nil, err
		}
		files = append(files, resp.Files...)
		if resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}
	if len(files) > max {
		files = files[:max]
	}
	if files == nil {
		files = []json.RawMessage{}
	}
	return json.Marshal(files)
}

// exportMimeFor maps a Google Workspace mime type to the export format
// read_file requests for it.
var exportMimeFor = map[string]string{
	"application/vnd.google-apps.document":     "text/plain",
	"application/vnd.google-apps.spreadsheet":  "text/csv",
	"application/vnd.google-apps.presentation": "text/plain",
}

type owner struct {
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
}

type fileMeta struct {
	ID           string  `json:"id"`
	Name         string  `json:"name"`
	MimeType     string  `json:"mimeType"`
	Owners       []owner `json:"owners"`
	ModifiedTime string  `json:"modifiedTime"`
	WebViewLink  string  `json:"webViewLink"`
}

type fileContent struct {
	fileMeta
	Content   string `json:"content,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
	Note      string `json:"note,omitempty"`
}

func (c *Drive) readFile(ctx context.Context, cl *gapi.Client, args map[string]any) (json.RawMessage, error) {
	id := gapi.ArgString(args, "id")
	if id == "" {
		return nil, errors.New("gdrive: read_file requires id")
	}
	var meta fileMeta
	metaFields := url.Values{"fields": {"id,name,mimeType,owners,modifiedTime,webViewLink"}}
	if err := cl.GetJSON(ctx, gapi.DriveBase+"/files/"+url.PathEscape(id), metaFields, &meta); err != nil {
		return nil, err
	}
	out := fileContent{fileMeta: meta}
	switch {
	case exportMimeFor[meta.MimeType] != "":
		text, truncated, err := cl.GetText(ctx, gapi.DriveBase+"/files/"+url.PathEscape(id)+"/export",
			url.Values{"mimeType": {exportMimeFor[meta.MimeType]}}, maxContentBytes)
		if err != nil {
			return nil, err
		}
		out.Content, out.Truncated = text, truncated
	case meta.MimeType == "text/plain" || meta.MimeType == "text/markdown":
		text, truncated, err := cl.GetText(ctx, gapi.DriveBase+"/files/"+url.PathEscape(id),
			url.Values{"alt": {"media"}}, maxContentBytes)
		if err != nil {
			return nil, err
		}
		out.Content, out.Truncated = text, truncated
	default:
		out.Note = "content not available for mime type " + meta.MimeType + "; metadata only"
	}
	return json.Marshal(out)
}

func ownerName(owners []owner) string {
	if len(owners) == 0 {
		return ""
	}
	if owners[0].DisplayName != "" {
		return owners[0].DisplayName
	}
	return owners[0].EmailAddress
}

func excerpt(s string) string {
	r := []rune(s)
	if len(r) > 280 {
		r = r[:280]
	}
	return string(r)
}

func (c *Drive) Normalize(function string, raw json.RawMessage) ([]store.Record, error) {
	switch function {
	case "search_files":
		var files []fileMeta
		if err := json.Unmarshal(raw, &files); err != nil {
			return nil, err
		}
		out := make([]store.Record, 0, len(files))
		for _, f := range files {
			modified, _ := time.Parse(time.RFC3339, f.ModifiedTime)
			out = append(out, &store.Document{
				Meta:       store.Meta{Source: c.Name(), SourceID: f.ID, External: true},
				Title:      f.Name,
				URL:        f.WebViewLink,
				MimeType:   f.MimeType,
				Owner:      ownerName(f.Owners),
				ModifiedAt: modified.UTC(),
			})
		}
		return out, nil
	case "read_file":
		var f fileContent
		if err := json.Unmarshal(raw, &f); err != nil {
			return nil, err
		}
		modified, _ := time.Parse(time.RFC3339, f.ModifiedTime)
		return []store.Record{&store.Document{
			Meta:       store.Meta{Source: c.Name(), SourceID: f.ID, External: true},
			Title:      f.Name,
			URL:        f.WebViewLink,
			MimeType:   f.MimeType,
			Owner:      ownerName(f.Owners),
			Excerpt:    excerpt(f.Content),
			ModifiedAt: modified.UTC(),
		}}, nil
	}
	return nil, nil
}
