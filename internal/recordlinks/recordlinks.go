// Package recordlinks writes the deterministic workspace links Slice V's D6
// allows (docs/slices/V.md §7.3 D6, §7.4 V-links): edges that follow from
// code and stored records alone, never from a model's guess.
//
//   - meeting -> involves -> person, from the calendar event's attendees;
//   - decision -> involves -> person, from the card's source message sender;
//   - thread -> the anchor's own involves/for_project edges, copied.
//
// A person is only ever named through roster.PersonByIdentity on the
// "email" identity key, with an exact match on the address (or on its
// lowercase form, since the domain and, in practice, the local part of an
// address are case-insensitive). An identity that doesn't resolve writes
// nothing: no fuzzy name matching, no display-name lookup, no fallback.
// Every write goes through store.AddLink, which is idempotent, so a writer
// re-running over the same record never duplicates an edge.
package recordlinks

import (
	"context"
	"errors"
	"net/mail"
	"sort"
	"strings"

	"water/internal/roster"
	"water/internal/store"
)

// EmailIdentity is the roster identity key an email address resolves
// through (twins/ceo/seed/people.yaml's identities.email).
const EmailIdentity = "email"

// PersonType is the links node type for a roster person
// (internal/store/links.go's node-type list).
const PersonType = "person"

// address extracts the bare address from a calendar attendee or a message
// sender: either a plain "a@b.c" or a header value such as
// `"Ann Lee" <ann@b.c>`. Anything that doesn't parse as one address yields
// "" — it is never guessed at.
func address(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	a, err := mail.ParseAddress(raw)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(a.Address)
}

// PersonByAddress resolves raw (a bare address or a From-style header) to
// a roster person's id. ok is false when raw doesn't parse or no roster
// person carries that email identity; err is only a store failure.
func PersonByAddress(ctx context.Context, st *store.Store, raw string) (id string, ok bool, err error) {
	addr := address(raw)
	if addr == "" {
		return "", false, nil
	}
	candidates := []string{addr}
	if lower := strings.ToLower(addr); lower != addr {
		candidates = append(candidates, lower)
	}
	for _, c := range candidates {
		p, err := roster.PersonByIdentity(ctx, st, EmailIdentity, c)
		if errors.Is(err, roster.ErrNoSuchIdentity) {
			continue
		}
		if err != nil {
			return "", false, err
		}
		if p.SourceID == "" {
			continue
		}
		return p.SourceID, true, nil
	}
	return "", false, nil
}

// ResolvePeople resolves every address in raws to a roster person id,
// dropping the ones that don't resolve. The result is sorted and has no
// duplicates.
func ResolvePeople(ctx context.Context, st *store.Store, raws []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, r := range raws {
		id, ok, err := PersonByAddress(ctx, st, r)
		if err != nil {
			return nil, err
		}
		if ok && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

// Involve writes fromType/fromID -> involves -> person for each person id.
func Involve(ctx context.Context, st *store.Store, fromType, fromID string, people []string) error {
	if fromType == "" || fromID == "" {
		return nil
	}
	for _, p := range people {
		if err := st.AddLink(ctx, store.Link{Kind: store.LinkInvolves, FromType: fromType, FromID: fromID, ToType: PersonType, ToID: p}); err != nil {
			return err
		}
	}
	return nil
}

// InvolveAddresses resolves raws and writes an involves edge from
// fromType/fromID to each person that resolves. It returns the linked
// person ids.
func InvolveAddresses(ctx context.Context, st *store.Store, fromType, fromID string, raws []string) ([]string, error) {
	people, err := ResolvePeople(ctx, st, raws)
	if err != nil {
		return nil, err
	}
	return people, Involve(ctx, st, fromType, fromID, people)
}

// SourceSenders returns the sender of every stored message among refs
// ("source:source_id", decisions.Card.SourceItemIDs' format). A ref that
// isn't a stored message (a document, a calendar event, a record that has
// since gone) contributes nothing.
func SourceSenders(ctx context.Context, st *store.Store, refs []string) ([]string, error) {
	var out []string
	for _, ref := range refs {
		source, sourceID, ok := strings.Cut(ref, ":")
		if !ok || source == "" || sourceID == "" {
			continue
		}
		m, err := store.Get[store.Message](ctx, st, source, sourceID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if m.From != "" {
			out = append(out, m.From)
		}
	}
	return out, nil
}

// DecisionType is the links node type for a decision card.
const DecisionType = "decision"

// LinkDecision writes decision(cardID) -> involves -> person for the
// sender of each of the card's source messages that the roster resolves.
func LinkDecision(ctx context.Context, st *store.Store, cardID string, sourceItemIDs []string) ([]string, error) {
	if cardID == "" {
		return nil, nil
	}
	senders, err := SourceSenders(ctx, st, sourceItemIDs)
	if err != nil {
		return nil, err
	}
	return InvolveAddresses(ctx, st, DecisionType, cardID, senders)
}

// copiedKinds are the edge kinds a thread inherits from its anchor.
var copiedKinds = []string{store.LinkInvolves, store.LinkForProject}

// CopyLinks gives dst every involves/for_project edge src has, pointing at
// the same records. Only src's outgoing edges are copied; nothing is
// derived.
func CopyLinks(ctx context.Context, st *store.Store, srcType, srcID, dstType, dstID string) error {
	if srcType == "" || srcID == "" || dstType == "" || dstID == "" {
		return nil
	}
	for _, kind := range copiedKinds {
		ls, err := st.LinksFrom(ctx, srcType, srcID, kind)
		if err != nil {
			return err
		}
		for _, l := range ls {
			if err := st.AddLink(ctx, store.Link{Kind: kind, FromType: dstType, FromID: dstID, ToType: l.ToType, ToID: l.ToID}); err != nil {
				return err
			}
		}
	}
	return nil
}
