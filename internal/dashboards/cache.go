package dashboards

import (
	"sync"
	"time"
)

// DefaultCacheTTL is the TTL docs/slices/UI.md Phase 4 specifies for every
// dashboard compute function's underlying connector call.
const DefaultCacheTTL = 10 * time.Minute

// Cache is a keyed, TTL cache in front of every dashboard compute
// function's gated connector call, mirroring decisions.Trigger's own
// CardTTL design (commit 4a47519, "Proper rate-cap fix: cache decision
// cards and reserve gate headroom for P0"): a value is reused until it is
// TTL old, and a per-key lock held for the whole fetch means racing
// callers on a stale or missing entry share one call instead of each
// making their own gated call.
//
// Unlike Trigger's single cached report, this cache holds one entry per
// key, because a dashboard's tiles fan out to several independent
// connector calls (finance alone reads cash_position, spend_breakdown_all
// and monthly_costs) that don't share one build and don't all go stale at
// the same moment. The key is the underlying source call's own identity
// (e.g. "company_finance.cash_position"), not the dashboard or the metric/
// breakdown/callout id — several tiles read the same key (finance's three
// metrics all key on "company_finance.cash_position"), which is exactly
// what makes "N dashboard renders within the TTL cost at most one gated
// call per underlying source" true.
//
// A failed fetch is cached too, unlike Trigger's card report (which
// deliberately never caches an incomplete pass, since a card's absence
// would otherwise look like "no decision needed" instead of "the build
// failed"). A dashboard tile already carries an explicit not_connected/
// unavailable state distinct from a wrong number, so reusing a failure for
// the same TTL as a success is safe, and is what keeps a steady-state
// not_connected source (the common case here: company_customers before
// the owner uploads its spreadsheet) from being re-hit by the gate on
// every render.
type Cache struct {
	// TTL is how long a cached value is reused. TTL <= 0 disables caching
	// entirely (every call fetches fresh), matching decisions.Trigger's
	// own CardTTL <= 0 convention.
	TTL time.Duration

	mu    sync.Mutex
	items map[string]*cacheItem
}

type cacheItem struct {
	// mu is held for the whole check-then-fetch, so concurrent callers
	// racing a stale or missing entry serialize into one fetch: the first
	// one through fetches and stores; every other one then sees a fresh
	// entry and reuses it instead of also calling fetch.
	mu        sync.Mutex
	val       any
	err       error
	fetchedAt time.Time
	valid     bool
}

func (c *Cache) item(key string) *cacheItem {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = map[string]*cacheItem{}
	}
	it, ok := c.items[key]
	if !ok {
		it = &cacheItem{}
		c.items[key] = it
	}
	return it
}

// Get returns key's cached value (or error) when it was fetched within c's
// TTL of now, otherwise it calls fetch once and caches whatever it
// returns, success or failure. now is a parameter (not time.Now()
// directly) so a test can drive the TTL window deterministically, and so
// every tile in one Compute.Dashboard render agrees on "now" the same way
// decisions.Trigger's Run(ctx, now) does.
//
// Get is a package-level function, not a *Cache method, because Go methods
// cannot introduce their own type parameters.
func Get[T any](c *Cache, now time.Time, key string, fetch func() (T, error)) (T, error) {
	if c == nil || c.TTL <= 0 {
		return fetch()
	}
	it := c.item(key)
	it.mu.Lock()
	defer it.mu.Unlock()
	if it.valid && !now.Before(it.fetchedAt) && now.Sub(it.fetchedAt) < c.TTL {
		if it.err != nil {
			var zero T
			return zero, it.err
		}
		return it.val.(T), nil
	}
	v, err := fetch()
	it.val, it.err, it.fetchedAt, it.valid = v, err, now, true
	return v, err
}
