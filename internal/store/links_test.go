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

func TestAddLinkInWorkspaceRoundTrips(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	if err := s.AddLink(ctx, Link{Kind: LinkInWorkspace, FromType: "decision", FromID: "d-1", ToType: "workspace", ToID: "ws-main"}); err != nil {
		t.Fatal(err)
	}

	from, err := s.LinksFrom(ctx, "decision", "d-1", LinkInWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(from) != 1 || from[0].ToType != "workspace" || from[0].ToID != "ws-main" {
		t.Fatalf("LinksFrom = %+v", from)
	}
}

func TestAddLinkNonAllocatedFractionStaysZero(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	// Fraction is only meaningful for LinkAllocated; nullableFraction must
	// still store NULL (and read back as 0) for any other kind, even if a
	// caller mistakenly sets it on the struct.
	if err := s.AddLink(ctx, Link{Kind: LinkAbout, FromType: "thread", FromID: "t-1", ToType: "decision", ToID: "d-1", Fraction: 0.9}); err != nil {
		t.Fatal(err)
	}

	from, err := s.LinksFrom(ctx, "thread", "t-1", LinkAbout)
	if err != nil {
		t.Fatal(err)
	}
	if len(from) != 1 || from[0].Fraction != 0 {
		t.Fatalf("LinksFrom = %+v, want Fraction 0", from)
	}
}

func TestLinksOfFindsEitherSideWithoutDuplicating(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	if err := s.AddLink(ctx, Link{Kind: LinkInWorkspace, FromType: "decision", FromID: "d-1", ToType: "workspace", ToID: "ws-main"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddLink(ctx, Link{Kind: LinkInvolves, FromType: "decision", FromID: "d-1", ToType: "person", ToID: "theo"}); err != nil {
		t.Fatal(err)
	}

	fromSide, err := s.LinksOf(ctx, "decision", "d-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(fromSide) != 2 {
		t.Fatalf("LinksOf(decision, d-1) = %+v, want 2 edges", fromSide)
	}

	toSide, err := s.LinksOf(ctx, "workspace", "ws-main")
	if err != nil {
		t.Fatal(err)
	}
	if len(toSide) != 1 || toSide[0].FromID != "d-1" {
		t.Fatalf("LinksOf(workspace, ws-main) = %+v", toSide)
	}
}

func TestRemoveLinkDeletesAndIsIdempotent(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	l := Link{Kind: LinkAbout, FromType: "thread", FromID: "t-2", ToType: "meeting", ToID: "m-1"}
	if err := s.AddLink(ctx, l); err != nil {
		t.Fatal(err)
	}

	if err := s.RemoveLink(ctx, l); err != nil {
		t.Fatal(err)
	}

	from, err := s.LinksFrom(ctx, "thread", "t-2", LinkAbout)
	if err != nil {
		t.Fatal(err)
	}
	if len(from) != 0 {
		t.Fatalf("LinksFrom after RemoveLink = %+v, want empty", from)
	}
	of, err := s.LinksOf(ctx, "thread", "t-2")
	if err != nil {
		t.Fatal(err)
	}
	if len(of) != 0 {
		t.Fatalf("LinksOf after RemoveLink = %+v, want empty", of)
	}

	// Removing an edge that was never there (or already removed) is a
	// no-op, not an error.
	if err := s.RemoveLink(ctx, l); err != nil {
		t.Fatalf("RemoveLink on non-existent edge: %v", err)
	}
}

func TestRosterLinksStillRoundTripAfterVChanges(t *testing.T) {
	// Regression check: the existing roster kinds (member_of etc.) still
	// work exactly as before, unaffected by the new Slice V kinds/methods.
	s, _ := openTemp(t)
	ctx := context.Background()

	if err := s.AddLink(ctx, Link{Kind: LinkMemberOf, FromType: "person", FromID: "nina", ToType: "team", ToID: "ranking"}); err != nil {
		t.Fatal(err)
	}

	got, err := s.LinksFrom(ctx, "person", "nina", LinkMemberOf)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ToID != "ranking" {
		t.Fatalf("LinksFrom(member_of) = %+v", got)
	}
}
