package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"
)

// Meta is common to every normalized record. (Source, SourceID) is the
// identity: re-ingesting the same upstream item updates it in place.
// External marks content written by someone other than the CEO (mail,
// chat, docs, web); it is untrusted data wherever it flows.
type Meta struct {
	Source    string    `db:"source"`
	SourceID  string    `db:"source_id"`
	External  bool      `db:"external"`
	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`
}

func (m *Meta) meta() *Meta { return m }

// Record is implemented only by the normalized types in this package.
type Record interface {
	Table() string
	meta() *Meta
}

type Message struct {
	Meta
	Channel string   `db:"channel"`
	Thread  string   `db:"thread"`
	From    string   `db:"sender"`
	To      []string `db:"recipients"`
	Subject string   `db:"subject"`
	Body    string   `db:"body"`
	// BodyFull marks Body as the message's full text rather than a preview
	// (e.g. a Gmail snippet). Once a full body is stored, a later upsert
	// carrying only a preview keeps the full body instead of downgrading it.
	BodyFull bool      `db:"body_full"`
	SentAt   time.Time `db:"sent_at"`
}

type Meeting struct {
	Meta
	Title         string    `db:"title"`
	StartAt       time.Time `db:"start_at"`
	EndAt         time.Time `db:"end_at"`
	Attendees     []string  `db:"attendees"`
	Organizer     string    `db:"organizer"`
	TranscriptRef string    `db:"transcript_ref"`
	Summary       string    `db:"summary"`
}

type Event struct {
	Meta
	Title     string    `db:"title"`
	StartAt   time.Time `db:"start_at"`
	EndAt     time.Time `db:"end_at"`
	Location  string    `db:"location"`
	Attendees []string  `db:"attendees"`
	Organizer string    `db:"organizer"`
	Status    string    `db:"status"`
}

// AllDay reports whether e spans whole local days: it starts at local
// midnight and ends at a later local midnight. Calendar connectors store an
// all-day event (a date, not a dateTime) exactly that way.
func (e Event) AllDay() bool {
	if e.StartAt.IsZero() || e.EndAt.IsZero() {
		return false
	}
	st, en := e.StartAt.Local(), e.EndAt.Local()
	return en.After(st) && isLocalMidnight(st) && isLocalMidnight(en)
}

func isLocalMidnight(t time.Time) bool {
	return t.Hour() == 0 && t.Minute() == 0 && t.Second() == 0 && t.Nanosecond() == 0
}

// Clock is e's local start time for a one-line schedule ("09:30"), or
// "all day" for an all-day event, so a date-only event never shows a
// made-up "00:00".
func (e Event) Clock() string {
	if e.AllDay() {
		return "all day"
	}
	return e.StartAt.Local().Format("15:04")
}

type Document struct {
	Meta
	Title      string    `db:"title"`
	URL        string    `db:"url"`
	MimeType   string    `db:"mime_type"`
	Owner      string    `db:"owner"`
	Excerpt    string    `db:"excerpt"`
	ModifiedAt time.Time `db:"modified_at"`

	// Content is a read's full text (up to the connector's own size cap)
	// and Truncated says that cap cut it short. They carry a read_file
	// result to its caller in memory only: neither has a db tag, so neither
	// is persisted, and a Document loaded from the store has only Excerpt.
	Content   string
	Truncated bool
}

type Issue struct {
	Meta
	Title    string `db:"title"`
	State    string `db:"state"`
	Assignee string `db:"assignee"`
	Project  string `db:"project"`
	Priority string `db:"priority"`
	URL      string `db:"url"`
}

type Commit struct {
	Meta
	Repo        string    `db:"repo"`
	SHA         string    `db:"sha"`
	Author      string    `db:"author"`
	Message     string    `db:"message"`
	CommittedAt time.Time `db:"committed_at"`
	URL         string    `db:"url"`
}

type Transaction struct {
	Meta
	Account      string    `db:"account"`
	AmountMinor  int64     `db:"amount_minor"`
	Currency     string    `db:"currency"`
	Counterparty string    `db:"counterparty"`
	Description  string    `db:"description"`
	PostedAt     time.Time `db:"posted_at"`
}

type Contact struct {
	Meta
	Name  string `db:"name"`
	Email string `db:"email"`
	Phone string `db:"phone"`
	Org   string `db:"org"`
	Title string `db:"title"`
}

func (*Message) Table() string     { return "messages" }
func (*Meeting) Table() string     { return "meetings" }
func (*Event) Table() string       { return "events" }
func (*Document) Table() string    { return "documents" }
func (*Issue) Table() string       { return "issues" }
func (*Commit) Table() string      { return "commits" }
func (*Transaction) Table() string { return "transactions" }
func (*Contact) Table() string     { return "contacts" }

type column struct {
	name string
	v    reflect.Value
}

func columns(r Record) []column {
	var out []column
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.Anonymous {
				walk(v.Field(i))
				continue
			}
			if name := f.Tag.Get("db"); name != "" {
				out = append(out, column{name, v.Field(i)})
			}
		}
	}
	walk(reflect.ValueOf(r).Elem())
	return out
}

var timeType = reflect.TypeFor[time.Time]()

func encode(v reflect.Value) (any, error) {
	switch {
	case v.Type() == timeType:
		t := v.Interface().(time.Time)
		if t.IsZero() {
			return nil, nil
		}
		return t.UnixNano(), nil
	case v.Kind() == reflect.Slice && v.Type().Elem().Kind() == reflect.String:
		if v.Len() == 0 {
			return "[]", nil
		}
		b, err := json.Marshal(v.Interface())
		return string(b), err
	case v.Kind() == reflect.Bool:
		if v.Bool() {
			return int64(1), nil
		}
		return int64(0), nil
	case v.Kind() == reflect.String:
		return v.String(), nil
	case v.Kind() == reflect.Int64:
		return v.Int(), nil
	}
	return nil, fmt.Errorf("store: unsupported field type %s", v.Type())
}

// scanTarget returns a destination for Scan and a func that copies it into v.
func scanTarget(v reflect.Value) (any, func() error) {
	switch {
	case v.Type() == timeType:
		var n sql.NullInt64
		return &n, func() error {
			if n.Valid {
				v.Set(reflect.ValueOf(time.Unix(0, n.Int64).UTC()))
			} else {
				v.Set(reflect.Zero(timeType))
			}
			return nil
		}
	case v.Kind() == reflect.Slice:
		var s string
		return &s, func() error {
			var out []string
			if err := json.Unmarshal([]byte(s), &out); err != nil {
				return err
			}
			if len(out) == 0 {
				out = nil
			}
			v.Set(reflect.ValueOf(out))
			return nil
		}
	case v.Kind() == reflect.Bool:
		var n int64
		return &n, func() error { v.SetBool(n != 0); return nil }
	default:
		return v.Addr().Interface(), func() error { return nil }
	}
}

// Upsert inserts r or, when (source, source_id) exists, replaces its fields.
// Two exceptions: a record type with a body_full column (Message) never has
// a stored full body replaced by an incoming preview, and a Document never
// has a stored excerpt replaced by an empty one (a search hit carries none);
// the other fields still update.
func (s *Store) Upsert(ctx context.Context, r Record) error {
	m := r.meta()
	if m.Source == "" || m.SourceID == "" {
		return errors.New("store: record needs source and source_id")
	}
	now := time.Now().UTC()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	if m.UpdatedAt.IsZero() {
		m.UpdatedAt = now
	}
	cols := columns(r)
	names := make([]string, len(cols))
	marks := make([]string, len(cols))
	var sets []string
	args := make([]any, len(cols))
	for i, c := range cols {
		v, err := encode(c.v)
		if err != nil {
			return err
		}
		names[i], marks[i], args[i] = c.name, "?", v
		switch c.name {
		case "source", "source_id", "created_at":
		case "body":
			if hasColumn(cols, "body_full") {
				sets = append(sets, fmt.Sprintf("body = CASE WHEN excluded.body_full = 0 AND %s.body_full = 1 THEN %[1]s.body ELSE excluded.body END", r.Table()))
				continue
			}
			sets = append(sets, "body = excluded.body")
		case "body_full":
			sets = append(sets, fmt.Sprintf("body_full = MAX(%s.body_full, excluded.body_full)", r.Table()))
		case "excerpt":
			// A search hit carries no excerpt; it must not wipe the one a
			// read stored (documents_fts indexes it for meeting help/cues).
			sets = append(sets, fmt.Sprintf("excerpt = CASE WHEN excluded.excerpt = '' THEN %s.excerpt ELSE excluded.excerpt END", r.Table()))
		default:
			sets = append(sets, c.name+" = excluded."+c.name)
		}
	}
	q := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT (source, source_id) DO UPDATE SET %s",
		r.Table(), strings.Join(names, ", "), strings.Join(marks, ", "), strings.Join(sets, ", "))
	_, err := s.db.ExecContext(ctx, q, args...)
	return err
}

func hasColumn(cols []column, name string) bool {
	for _, c := range cols {
		if c.name == name {
			return true
		}
	}
	return false
}

// Query filters a List. Times bound created_at; zero means unbounded.
type Query struct {
	Source string
	Since  time.Time
	Until  time.Time
	Limit  int
}

// Get returns one record by its upstream identity.
func Get[T any, P interface {
	*T
	Record
}](ctx context.Context, s *Store, source, sourceID string) (*T, error) {
	var zero T
	out, err := list[T, P](ctx, s, "source = ? AND source_id = ?", []any{source, sourceID}, 1, P(&zero).Table())
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return &out[0], nil
}

// List returns records newest first.
func List[T any, P interface {
	*T
	Record
}](ctx context.Context, s *Store, q Query) ([]T, error) {
	var zero T
	var where []string
	var args []any
	if q.Source != "" {
		where, args = append(where, "source = ?"), append(args, q.Source)
	}
	if !q.Since.IsZero() {
		where, args = append(where, "created_at >= ?"), append(args, q.Since.UnixNano())
	}
	if !q.Until.IsZero() {
		where, args = append(where, "created_at < ?"), append(args, q.Until.UnixNano())
	}
	cond := "1 = 1"
	if len(where) > 0 {
		cond = strings.Join(where, " AND ")
	}
	return list[T, P](ctx, s, cond, args, q.Limit, P(&zero).Table())
}

// EventsInRange returns events whose start_at falls in [from, to), earliest
// first. Unlike List (which pages by created_at, newest first), this is what
// "what's on my calendar" needs: an event ingested long ago that starts
// today must still show up, even if 500 other records were created more
// recently.
func EventsInRange(ctx context.Context, s *Store, from, to time.Time) ([]Event, error) {
	var probe Event
	cols := columns(&probe)
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.name
	}
	q := fmt.Sprintf("SELECT %s FROM events WHERE start_at >= ? AND start_at < ? ORDER BY start_at ASC", strings.Join(names, ", "))
	rows, err := s.db.QueryContext(ctx, q, from.UnixNano(), to.UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var rec Event
		cols := columns(&rec)
		dests := make([]any, len(cols))
		fills := make([]func() error, len(cols))
		for i, c := range cols {
			dests[i], fills[i] = scanTarget(c.v)
		}
		if err := rows.Scan(dests...); err != nil {
			return nil, err
		}
		for _, f := range fills {
			if err := f(); err != nil {
				return nil, err
			}
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// MessagesInRange returns messages sent in [from, to), newest first. It is
// what "new mail since yesterday" needs: List's Since bounds created_at (when
// the row was ingested), so old mail pulled in today by a prefetch, a model
// search or a first backfill would count as new. A message with no sent_at
// (a connector that could not tell) falls back to its ingest time. A zero to
// leaves the range open-ended.
func MessagesInRange(ctx context.Context, s *Store, from, to time.Time) ([]Message, error) {
	cond := "COALESCE(sent_at, created_at) >= ?"
	args := []any{from.UnixNano()}
	if !to.IsZero() {
		cond += " AND COALESCE(sent_at, created_at) < ?"
		args = append(args, to.UnixNano())
	}
	return listOrdered[Message](ctx, s, cond, args, 0, "messages", "COALESCE(sent_at, created_at) DESC, id DESC")
}

func list[T any, P interface {
	*T
	Record
}](ctx context.Context, s *Store, cond string, args []any, limit int, table string) ([]T, error) {
	return listOrdered[T, P](ctx, s, cond, args, limit, table, "created_at DESC, id DESC")
}

func listOrdered[T any, P interface {
	*T
	Record
}](ctx context.Context, s *Store, cond string, args []any, limit int, table, order string) ([]T, error) {
	var probe T
	cols := columns(P(&probe))
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = c.name
	}
	q := fmt.Sprintf("SELECT %s FROM %s WHERE %s ORDER BY %s", strings.Join(names, ", "), table, cond, order)
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		var rec T
		cols := columns(P(&rec))
		dests := make([]any, len(cols))
		fills := make([]func() error, len(cols))
		for i, c := range cols {
			dests[i], fills[i] = scanTarget(c.v)
		}
		if err := rows.Scan(dests...); err != nil {
			return nil, err
		}
		for _, f := range fills {
			if err := f(); err != nil {
				return nil, err
			}
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}
