package approvals

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"water/internal/spokenemail"
)

// ErrRecipient wraps every recipient refusal Propose returns: an address
// that is not a valid email address, or a domain that looks like a misheard
// provider or company domain (docs/slices/W.md D4c). Nothing is proposed or
// audited when it is returned.
var ErrRecipient = errors.New("recipient check")

// confirmUnusualKey is the optional payload key a gmail write carries once
// the CEO confirmed an unusual, spelled-out address: it turns a near-miss
// refusal into a warning (the envelope then stays tap-only).
const confirmUnusualKey = "confirm_unusual_recipient"

// recipientActions are the actions whose payload names mail recipients.
var recipientActions = map[string]bool{
	"gmail.send_message":     true,
	"gmail.draft_message":    true,
	"gmail.draft_for_review": true,
}

// RecipientsOf returns the addresses a gmail write would mail (to, cc,
// bcc), as bare addresses; nil for every other action.
func RecipientsOf(action string, payload map[string]any) []string {
	var out []string
	for _, a := range recipientItems(action, payload) {
		out = append(out, spokenemail.Address(a))
	}
	return out
}

// recipientItems returns a gmail write's to/cc/bcc items exactly as given
// (trimmed), before any display name is stripped: the checks must see the
// whole item, since the whole item is what goes into the header.
func recipientItems(action string, payload map[string]any) []string {
	if !recipientActions[action] {
		return nil
	}
	var out []string
	for _, k := range []string{"to", "cc", "bcc"} {
		for _, a := range listItems(payload[k]) {
			if a = strings.TrimSpace(a); a != "" {
				out = append(out, a)
			}
		}
	}
	return out
}

// MXResolver is the DNS the checker needs; net.DefaultResolver satisfies it
// and tests pass a fake.
type MXResolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupHost(ctx context.Context, host string) ([]string, error)
}

// NetResolver is the system resolver.
func NetResolver() MXResolver { return net.DefaultResolver }

// mxState is what a lookup found for one domain.
type mxState int

const (
	mxOK         mxState = iota + 1 // an MX, or (RFC 5321 implicit MX) an address record
	mxNone                          // no MX and no address record, or a null MX
	mxUnverified                    // the lookup failed or timed out
)

type mxEntry struct {
	state mxState
	at    time.Time
}

// Cache lifetimes and bounds for MX results.
const (
	mxTimeout     = 1500 * time.Millisecond
	mxTTLOK       = 6 * time.Hour
	mxTTLNone     = 10 * time.Minute
	mxTTLError    = time.Minute
	mxCacheLimit  = 512
	mxWarmTimeout = 5 * time.Second
)

// RecipientChecker runs the recipient checks before an envelope for a gmail
// write is proposed: the pure checks (spokenemail.CheckRecipient: syntax,
// near-miss against public providers and the company's internal domains)
// and an MX lookup with a short timeout, cached in process. A nil
// *RecipientChecker runs the pure checks only and never touches DNS.
type RecipientChecker struct {
	r        MXResolver
	internal []string
	now      func() time.Time
	timeout  time.Duration

	mu       sync.Mutex
	cache    map[string]mxEntry
	inflight map[string]bool
	warming  sync.WaitGroup
}

// NewRecipientChecker builds a checker that resolves through r (nil: no
// DNS) and treats internalDomains as known company domains.
func NewRecipientChecker(r MXResolver, internalDomains []string) *RecipientChecker {
	var internal []string
	for _, d := range internalDomains {
		if d = strings.ToLower(strings.TrimSpace(d)); d != "" {
			internal = append(internal, d)
		}
	}
	return &RecipientChecker{r: r, internal: internal, now: time.Now, timeout: mxTimeout,
		cache: map[string]mxEntry{}, inflight: map[string]bool{}}
}

func (c *RecipientChecker) known() []string {
	k := spokenemail.KnownProviders()
	if c != nil {
		k = append(k, c.internal...)
	}
	return k
}

// pure runs the checks that need no network: every address of a gmail
// write must be valid, and a near-miss is refused unless the payload says
// the CEO confirmed it. It returns the warnings (confirmed near-misses) and
// the bare domains still to be MX-checked, deduplicated in order.
func (c *RecipientChecker) pure(action string, payload map[string]any) (warnings, domains []string, err error) {
	items := recipientItems(action, payload)
	if len(items) == 0 {
		return nil, nil, nil
	}
	confirmed, _ := payload[confirmUnusualKey].(bool)
	known := c.known()
	seen := map[string]bool{}
	for _, item := range items {
		w, err := spokenemail.CheckRecipient(item, known, confirmed)
		if err != nil {
			return nil, nil, fmt.Errorf("%w: %s", ErrRecipient, err.Error())
		}
		a := spokenemail.Address(item)
		if w != "" {
			warnings = append(warnings, w)
		}
		d := strings.ToLower(a[strings.LastIndexByte(a, '@')+1:])
		if !seen[d] && !skipMX(d) {
			seen[d] = true
			domains = append(domains, d)
		}
	}
	return warnings, domains, nil
}

// skipMX: the big public providers always have mail servers, so looking
// them up only adds latency.
func skipMX(domain string) bool {
	for _, p := range spokenemail.KnownProviders() {
		if domain == p {
			return true
		}
	}
	return false
}

// Check is the propose-time check: the pure checks (a refusal is returned
// wrapped in ErrRecipient), then each remaining domain's MX, looked up now
// (bounded by a short timeout) unless cached. A domain with no mail server,
// or one that could not be verified, is a warning, never a refusal.
func (c *RecipientChecker) Check(ctx context.Context, action string, payload map[string]any) ([]string, error) {
	warnings, domains, err := c.pure(action, payload)
	if err != nil {
		return nil, err
	}
	if c == nil || c.r == nil {
		return warnings, nil
	}
	for _, d := range domains {
		st, ok := c.cached(d)
		if !ok {
			st = c.lookup(ctx, d, c.timeout)
			c.store(d, st)
		}
		if w := mxWarning(d, st); w != "" {
			warnings = append(warnings, w)
		}
	}
	return warnings, nil
}

// Warnings is the read-path variant for an envelope already in the queue:
// the pure checks' warnings plus MX status from the cache only. It never
// blocks on DNS; a domain not in the cache (after a daemon restart) reads
// "couldn't verify yet" and a background lookup is started for it.
func (c *RecipientChecker) Warnings(e Envelope) []string {
	warnings, domains, err := c.pure(e.Action, e.Payload)
	if err != nil {
		// An envelope from before these checks existed (or edited around
		// them) that fails them now: say so, prominently, rather than drop it.
		return []string{strings.TrimPrefix(err.Error(), ErrRecipient.Error()+": ")}
	}
	if c == nil || c.r == nil {
		return warnings
	}
	for _, d := range domains {
		st, ok := c.cached(d)
		if !ok {
			c.warm(d)
			warnings = append(warnings, "Couldn't verify the mail domain "+d+" yet")
			continue
		}
		if w := mxWarning(d, st); w != "" {
			warnings = append(warnings, w)
		}
	}
	return warnings
}

func mxWarning(domain string, st mxState) string {
	switch st {
	case mxNone:
		return domain + " has no mail server; the message would bounce"
	case mxUnverified:
		return "Couldn't verify the mail domain " + domain
	}
	return ""
}

func (c *RecipientChecker) cached(d string) (mxState, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[d]
	if !ok {
		return 0, false
	}
	ttl := mxTTLOK
	switch e.state {
	case mxNone:
		ttl = mxTTLNone
	case mxUnverified:
		ttl = mxTTLError
	}
	if c.now().Sub(e.at) >= ttl {
		delete(c.cache, d)
		return 0, false
	}
	return e.state, true
}

func (c *RecipientChecker) store(d string, st mxState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.cache[d]; !ok && len(c.cache) >= mxCacheLimit {
		// Evict the oldest entry: the cache only ever holds recent domains.
		var oldest string
		var at time.Time
		for k, v := range c.cache {
			if oldest == "" || v.at.Before(at) {
				oldest, at = k, v.at
			}
		}
		delete(c.cache, oldest)
	}
	c.cache[d] = mxEntry{state: st, at: c.now()}
}

// warm starts one background lookup for d unless one is already running.
func (c *RecipientChecker) warm(d string) {
	c.mu.Lock()
	if c.inflight[d] {
		c.mu.Unlock()
		return
	}
	c.inflight[d] = true
	c.warming.Add(1)
	c.mu.Unlock()
	go func() {
		defer c.warming.Done()
		st := c.lookup(context.Background(), d, mxWarmTimeout)
		c.store(d, st)
		c.mu.Lock()
		delete(c.inflight, d)
		c.mu.Unlock()
	}()
}

// wait blocks until every background lookup has finished (tests).
func (c *RecipientChecker) wait() { c.warming.Wait() }

// lookup resolves d's MX, falling back to an address record (RFC 5321's
// implicit MX). A null MX (RFC 7505: one record whose host is ".") means
// the domain accepts no mail.
func (c *RecipientChecker) lookup(ctx context.Context, d string, timeout time.Duration) mxState {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	mxs, err := c.r.LookupMX(ctx, d)
	if err == nil && len(mxs) > 0 {
		if len(mxs) == 1 && (mxs[0].Host == "." || mxs[0].Host == "") {
			return mxNone
		}
		return mxOK
	}
	if err != nil && !isNotFound(err) {
		return mxUnverified
	}
	hosts, err := c.r.LookupHost(ctx, d)
	if err == nil && len(hosts) > 0 {
		return mxOK
	}
	if err != nil && !isNotFound(err) {
		return mxUnverified
	}
	return mxNone
}

func isNotFound(err error) bool {
	var de *net.DNSError
	return errors.As(err, &de) && de.IsNotFound
}
