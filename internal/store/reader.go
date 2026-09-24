package store

import (
	"database/sql"
	"net/url"
)

// OpenReadOnly opens the twin's database (already created and migrated by
// Open) for reads only. It runs no migrations, and it never blocks behind
// the writer's single connection: it is meant for Slice R's reflex handlers,
// quick tools and speculative prefetch, which must stay fast and cheap even
// while the writer or a background sync is busy.
//
// mode=ro and _query_only(1) are defence in depth, not the only guard:
// internal/nervous's reflex/propose/tmpl/slots/intents/render/speak/turn
// packages are additionally forbidden, by an import/selector test, from
// calling anything that could mutate the store. A write attempt through this
// handle still fails at the SQLite level itself, so that guarantee doesn't
// depend solely on the Go-level review.
func OpenReadOnly(path string, conns int) (*Store, error) {
	q := url.Values{}
	q.Add("mode", "ro")
	for _, p := range []string{"busy_timeout(5000)", "journal_mode(WAL)"} {
		q.Add("_pragma", p)
	}
	q.Add("_query_only", "1")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	if conns > 0 {
		db.SetMaxOpenConns(conns)
	}
	return &Store{db: db}, nil
}
