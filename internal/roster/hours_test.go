package roster

import (
	"context"
	"testing"
	"testing/fstest"
	"time"
)

func d(s string) time.Time {
	t, err := time.Parse(dateLayout, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestHoursInvestedBeforeStartIsZero(t *testing.T) {
	got := HoursInvested(1.0, d("2026-08-18"), d("2026-08-01"))
	if got != 0 {
		t.Fatalf("HoursInvested before start = %v, want 0", got)
	}
}

func TestHoursInvestedZeroFractionIsZero(t *testing.T) {
	got := HoursInvested(0, d("2026-08-18"), d("2026-09-18"))
	if got != 0 {
		t.Fatalf("HoursInvested with 0 fraction = %v, want 0", got)
	}
}

func TestHoursInvestedExactlyOneWeekFullTime(t *testing.T) {
	got := HoursInvested(1.0, d("2026-08-18"), d("2026-08-25"))
	if got != 40 {
		t.Fatalf("HoursInvested 1 week full time = %v, want 40", got)
	}
}

func TestHoursInvestedFractionalAllocationAndWeeks(t *testing.T) {
	// 0.5 allocation, 2 weeks: 0.5 * 40 * 2 = 40.
	got := HoursInvested(0.5, d("2026-08-18"), d("2026-09-01"))
	if got != 40 {
		t.Fatalf("HoursInvested 0.5 allocation x 2 weeks = %v, want 40", got)
	}
}

func TestHoursInvestedZeroStartIsZero(t *testing.T) {
	got := HoursInvested(1.0, time.Time{}, d("2026-09-01"))
	if got != 0 {
		t.Fatalf("HoursInvested with a zero start = %v, want 0 (never invent a start date)", got)
	}
}

func TestPersonProjectHoursResolvesFromStoreAndDeriveNeverStores(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	fsys := fstest.MapFS{
		"twins/ceo/seed/people.yaml": &fstest.MapFile{Data: []byte(minimalYAML)},
	}
	if err := Load(ctx, fsys, "ceo", st); err != nil {
		t.Fatal(err)
	}
	// lee is allocated 0.5 to ingestion-v2, which starts 2026-08-18. One
	// week later: 0.5 * 40 * 1 = 20.
	got, err := PersonProjectHours(ctx, st, "lee", "ingestion-v2", d("2026-08-25"))
	if err != nil {
		t.Fatal(err)
	}
	if got != 20 {
		t.Fatalf("PersonProjectHours(lee, ingestion-v2, +1wk) = %v, want 20", got)
	}

	// Asking again at a later "at" must give a different, larger number —
	// proof nothing was stored/cached from the call above.
	got2, err := PersonProjectHours(ctx, st, "lee", "ingestion-v2", d("2026-09-01"))
	if err != nil {
		t.Fatal(err)
	}
	if got2 != 40 {
		t.Fatalf("PersonProjectHours(lee, ingestion-v2, +2wk) = %v, want 40", got2)
	}
}

func TestPersonProjectHoursNotAllocatedIsZeroNotError(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	fsys := fstest.MapFS{
		"twins/ceo/seed/people.yaml": &fstest.MapFile{Data: []byte(minimalYAML)},
	}
	if err := Load(ctx, fsys, "ceo", st); err != nil {
		t.Fatal(err)
	}
	got, err := PersonProjectHours(ctx, st, "theo", "no-such-project-theo-is-not-on", d("2026-09-01"))
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("PersonProjectHours for an unallocated project = %v, want 0", got)
	}
}
