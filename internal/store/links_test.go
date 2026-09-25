package store

import (
	"context"
	"testing"
)

func TestAddLinkAndQueryBothDirections(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	if err := s.AddLink(ctx, Link{Kind: LinkAllocated, FromType: "person", FromID: "theo", ToType: "project", ToID: "ingestion-v2", Fraction: 1.0}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddLink(ctx, Link{Kind: LinkAllocated, FromType: "person", FromID: "nina", ToType: "project", ToID: "ranking-quality", Fraction: 1.0}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddLink(ctx, Link{Kind: LinkMemberOf, FromType: "person", FromID: "theo", ToType: "team", ToID: "crawler"}); err != nil {
		t.Fatal(err)
	}

	from, err := s.LinksFrom(ctx, "person", "theo", LinkAllocated)
	if err != nil {
		t.Fatal(err)
	}
	if len(from) != 1 || from[0].ToID != "ingestion-v2" || from[0].Fraction != 1.0 {
		t.Fatalf("LinksFrom = %+v", from)
	}

	to, err := s.LinksTo(ctx, "project", "ingestion-v2", LinkAllocated)
	if err != nil {
		t.Fatal(err)
	}
	if len(to) != 1 || to[0].FromID != "theo" {
		t.Fatalf("LinksTo = %+v", to)
	}

	// A different kind between the same two nodes never crosses over.
	memberOf, err := s.LinksFrom(ctx, "person", "theo", LinkMemberOf)
	if err != nil {
		t.Fatal(err)
	}
	if len(memberOf) != 1 || memberOf[0].ToID != "crawler" {
		t.Fatalf("LinksFrom(member_of) = %+v", memberOf)
	}
}

func TestAddLinkIsIdempotentAndUpdatesFraction(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	l := Link{Kind: LinkAllocated, FromType: "person", FromID: "dana", ToType: "project", ToID: "halcyon-rollout", Fraction: 0.7}
	if err := s.AddLink(ctx, l); err != nil {
		t.Fatal(err)
	}
	// Reloading the roster re-adds the same edge: must update in place, not
	// duplicate (a source of truth like people.yaml is re-loaded on every
	// daemon startup).
	l.Fraction = 0.5
	if err := s.AddLink(ctx, l); err != nil {
		t.Fatal(err)
	}

	got, err := s.LinksFrom(ctx, "person", "dana", LinkAllocated)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Fraction != 0.5 {
		t.Fatalf("got = %+v, want exactly one edge updated to fraction 0.5", got)
	}
}

func TestLinksFromEmptyIsNilNotError(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	got, err := s.LinksFrom(ctx, "person", "nobody", LinkLeads)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got = %+v, want empty", got)
	}
}
