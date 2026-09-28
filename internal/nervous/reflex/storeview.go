package reflex

import (
	"context"
	"time"

	"water/internal/nervous/slots"
	"water/internal/store"
)

// realStoreView adapts a *store.Store (opened read-only via
// store.OpenReadOnly in production) to StoreView.
type realStoreView struct {
	st *store.Store
}

// NewStoreView wraps st as a StoreView. Callers building the production
// Deps should pass a *store.Store opened with store.OpenReadOnly — this
// function does not open or check that itself, since tests legitimately
// want to exercise it against both a read-only and a writer handle (the
// read-only case is what actually enforces the "no mutation" invariant at
// the SQLite level; see internal/store/reader_test.go).
func NewStoreView(st *store.Store) StoreView {
	return realStoreView{st: st}
}

func (v realStoreView) EventsInRange(ctx context.Context, from, to time.Time) ([]store.Event, error) {
	return store.EventsInRange(ctx, v.st, from, to)
}

func (v realStoreView) NextEvent(ctx context.Context, after time.Time) (*store.Event, error) {
	return v.st.NextEvent(ctx, after)
}

func (v realStoreView) LatestMessages(ctx context.Context, limit int) ([]store.Message, error) {
	return v.st.LatestMessages(ctx, limit)
}

func (v realStoreView) MessagesFrom(ctx context.Context, email string, limit int) ([]store.Message, error) {
	return v.st.MessagesFrom(ctx, email, limit)
}

func (v realStoreView) CountMessagesSince(ctx context.Context, since time.Time) (int, error) {
	return v.st.CountMessagesSince(ctx, since)
}

func (v realStoreView) Senders(ctx context.Context, since time.Time, limit int) ([]slots.Person, error) {
	return v.st.Senders(ctx, since, limit)
}

func (v realStoreView) CursorUpdatedAt(ctx context.Context, key string) (time.Time, bool, error) {
	return v.st.CursorUpdatedAt(ctx, key)
}
