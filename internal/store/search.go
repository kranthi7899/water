package store

import (
	"context"
	"strings"
)

// MessageHit is one messages_fts match: enough to quote or point back at in
// a rendered answer without a further fetch.
type MessageHit struct {
	SourceID string
	From     string
	Subject  string
	External bool
}

// DocumentHit is one documents_fts match.
type DocumentHit struct {
	SourceID string
	Title    string
	External bool
}

// QuoteFTSTerm double-quotes term for safe inclusion in an FTS5 MATCH query,
// doubling any embedded double quote. Unquoted free text containing a
// character like ' % - : ( ) " or a bareword AND/OR/NOT/NEAR is an FTS5
// query error (internal/store/migrations/0007_meetings.sql's note); a
// caller building a query from untrusted transcript or message text must
// quote every term this way before joining them.
func QuoteFTSTerm(term string) string {
	return `"` + strings.ReplaceAll(term, `"`, `""`) + `"`
}

// SearchMessages runs an FTS5 MATCH query over messages_fts (subject, body,
// sender), local index only — no live connector call — ranked by bm25.
// query must already be FTS5-safe (build it with QuoteFTSTerm); an empty
// query returns no rows and no error.
func (s *Store) SearchMessages(ctx context.Context, query string, limit int) ([]MessageHit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT m.source_id, m.sender, m.subject, m.external FROM messages_fts f
		JOIN messages m ON m.id = f.rowid WHERE messages_fts MATCH ? ORDER BY bm25(messages_fts) LIMIT ?`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MessageHit
	for rows.Next() {
		var h MessageHit
		var ext int64
		if err := rows.Scan(&h.SourceID, &h.From, &h.Subject, &ext); err != nil {
			return nil, err
		}
		h.External = ext != 0
		out = append(out, h)
	}
	return out, rows.Err()
}

// SearchDocuments runs an FTS5 MATCH query over documents_fts (title,
// excerpt), local index only, ranked by bm25.
func (s *Store) SearchDocuments(ctx context.Context, query string, limit int) ([]DocumentHit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT d.source_id, d.title, d.external FROM documents_fts f
		JOIN documents d ON d.id = f.rowid WHERE documents_fts MATCH ? ORDER BY bm25(documents_fts) LIMIT ?`, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DocumentHit
	for rows.Next() {
		var h DocumentHit
		var ext int64
		if err := rows.Scan(&h.SourceID, &h.Title, &ext); err != nil {
			return nil, err
		}
		h.External = ext != 0
		out = append(out, h)
	}
	return out, rows.Err()
}
