package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func sampleRoute(turnID string, at time.Time) RouteRow {
	ackMS := int64(120)
	firstSentenceMS := int64(900)
	firstPartialLeadMS := int64(1500)
	return RouteRow{
		TurnID:                turnID,
		ClientTurnID:          "client-" + turnID,
		At:                    at,
		Channel:               "voice",
		Utterance:             "what's on my calendar today",
		TiersAttempted:        []string{"t0"},
		Owner:                 "quick",
		AnsweredBy:            "t0",
		Intent:                "schedule.on_date",
		IntentKind:            "read",
		IntentOrigin:          "embedded",
		Slots:                 map[string]string{"when": "today"},
		EscalationReason:      "",
		LatencyMS:             map[string]int64{"t0": 2},
		TotalMS:               5,
		Outcome:               "answered",
		Warnings:              []string{"voice_overlength"},
		Voice:                 true,
		Partials:              3,
		FirstPartialLeadMS:    &firstPartialLeadMS,
		Speculation:           map[string]any{"summary": true, "dry": "schedule.on_date"},
		SpeculationModelCalls: 0,
		AckMS:                 &ackMS,
		FirstSentenceMS:       &firstSentenceMS,
		ToolsUsed:             []string{"quick__calendar"},
		ToolsAttributed:       true,
		QuickOnly:             true,
		ToolSignature:         "quick.calendar(when)",
		Action:                map[string]string{"function": "", "decision": ""},
		PossibleMiss:          false,
		Confirmed:             false,
	}
}

func TestRouteRowRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	want := sampleRoute("turn-1", ts(10))
	id, err := s.InsertRoute(ctx, want)
	if err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListRoutes(ctx, ts(0), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	got := rows[0]
	if got.ID != id {
		t.Fatalf("ID = %d, want %d", got.ID, id)
	}
	if got.TurnID != want.TurnID || got.ClientTurnID != want.ClientTurnID || got.Channel != want.Channel ||
		got.Utterance != want.Utterance || got.Owner != want.Owner || got.AnsweredBy != want.AnsweredBy ||
		got.Intent != want.Intent || got.IntentKind != want.IntentKind || got.IntentOrigin != want.IntentOrigin ||
		got.EscalationReason != want.EscalationReason || got.TotalMS != want.TotalMS || got.Outcome != want.Outcome ||
		got.Voice != want.Voice || got.Partials != want.Partials || got.SpeculationModelCalls != want.SpeculationModelCalls ||
		got.ToolsAttributed != want.ToolsAttributed || got.QuickOnly != want.QuickOnly || got.ToolSignature != want.ToolSignature ||
		got.PossibleMiss != want.PossibleMiss || got.Confirmed != want.Confirmed {
		t.Fatalf("scalar fields mismatch:\n got %+v\nwant %+v", got, want)
	}
	if len(got.TiersAttempted) != 1 || got.TiersAttempted[0] != "t0" {
		t.Fatalf("TiersAttempted = %v", got.TiersAttempted)
	}
	if got.Slots["when"] != "today" {
		t.Fatalf("Slots = %v", got.Slots)
	}
	if got.LatencyMS["t0"] != 2 {
		t.Fatalf("LatencyMS = %v", got.LatencyMS)
	}
	if len(got.Warnings) != 1 || got.Warnings[0] != "voice_overlength" {
		t.Fatalf("Warnings = %v", got.Warnings)
	}
	if got.Speculation["dry"] != "schedule.on_date" {
		t.Fatalf("Speculation = %v", got.Speculation)
	}
	if len(got.ToolsUsed) != 1 || got.ToolsUsed[0] != "quick__calendar" {
		t.Fatalf("ToolsUsed = %v", got.ToolsUsed)
	}
	if got.Action["function"] != "" {
		t.Fatalf("Action = %v", got.Action)
	}
	if got.FirstPartialLeadMS == nil || *got.FirstPartialLeadMS != 1500 {
		t.Fatalf("FirstPartialLeadMS = %v", got.FirstPartialLeadMS)
	}
	if got.AckMS == nil || *got.AckMS != 120 {
		t.Fatalf("AckMS = %v", got.AckMS)
	}
	if got.FirstSentenceMS == nil || *got.FirstSentenceMS != 900 {
		t.Fatalf("FirstSentenceMS = %v", got.FirstSentenceMS)
	}
	if !got.At.Equal(want.At) {
		t.Fatalf("At = %v, want %v", got.At, want.At)
	}
}

func TestRouteRowNullableIntsAbsent(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	row := RouteRow{TurnID: "turn-2", At: ts(10), Channel: "cli", Utterance: "status", TiersAttempted: []string{"t0"}, Owner: "quick", Outcome: "answered", TotalMS: 1}
	if _, err := s.InsertRoute(ctx, row); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListRoutes(ctx, ts(0), 0)
	if err != nil {
		t.Fatal(err)
	}
	got := rows[0]
	if got.FirstPartialLeadMS != nil || got.AckMS != nil || got.FirstSentenceMS != nil {
		t.Fatalf("expected nil nullable ints, got %+v %+v %+v", got.FirstPartialLeadMS, got.AckMS, got.FirstSentenceMS)
	}
	if len(got.TiersAttempted) != 1 {
		t.Fatalf("TiersAttempted = %v", got.TiersAttempted)
	}
	if got.Slots == nil || len(got.Slots) != 0 {
		t.Fatalf("Slots default should be empty map, got %v", got.Slots)
	}
}

func TestMarkPossibleMiss(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	id, err := s.InsertRoute(ctx, sampleRoute("turn-3", ts(10)))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkPossibleMiss(ctx, id); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.ListRoutes(ctx, ts(0), 0)
	if !rows[0].PossibleMiss {
		t.Fatal("expected possible_miss=1 after MarkPossibleMiss")
	}
}

func TestListRoutesOrderAndSince(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if _, err := s.InsertRoute(ctx, sampleRoute("a", ts(8))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertRoute(ctx, sampleRoute("b", ts(10))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertRoute(ctx, sampleRoute("c", ts(12))); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListRoutes(ctx, ts(9), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].TurnID != "c" || rows[1].TurnID != "b" {
		t.Fatalf("ListRoutes since ts(9) = %v", rowIDs(rows))
	}
	limited, err := s.ListRoutes(ctx, ts(0), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 2 || limited[0].TurnID != "c" || limited[1].TurnID != "b" {
		t.Fatalf("ListRoutes limit=2 = %v", rowIDs(limited))
	}
}

func rowIDs(rows []RouteRow) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.TurnID
	}
	return out
}

func TestQuickOnlyRoutesFilters(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	quick := sampleRoute("quick1", ts(10))
	quick.Owner, quick.QuickOnly = "main", true
	notQuick := sampleRoute("notquick1", ts(11))
	notQuick.Owner, notQuick.QuickOnly = "main", false
	if _, err := s.InsertRoute(ctx, quick); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertRoute(ctx, notQuick); err != nil {
		t.Fatal(err)
	}
	rows, err := s.QuickOnlyRoutes(ctx, ts(0), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TurnID != "quick1" {
		t.Fatalf("QuickOnlyRoutes = %v", rowIDs(rows))
	}
}

// TestQuickOnlyRoutesRespectsLimit is a regression test: QuickOnlyRoutes
// had no limit parameter at all, so a caller scanning a wide lookback
// window on a heavy-usage daemon always pulled the entire quick_only result
// set into memory. A positive limit must cap the returned rows (newest
// first, matching ListRoutes' own limit semantics); 0 or negative means
// unlimited.
func TestQuickOnlyRoutesRespectsLimit(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		r := sampleRoute(fmt.Sprintf("q%d", i), ts(10+i))
		r.Owner, r.QuickOnly = "main", true
		if _, err := s.InsertRoute(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.QuickOnlyRoutes(ctx, ts(0), 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].TurnID != "q4" || rows[1].TurnID != "q3" {
		t.Fatalf("QuickOnlyRoutes(limit=2) = %v, want the 2 newest rows (q4, q3)", rowIDs(rows))
	}

	unlimited, err := s.QuickOnlyRoutes(ctx, ts(0), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(unlimited) != 5 {
		t.Fatalf("QuickOnlyRoutes(limit=0) = %d rows, want all 5 (0 means unlimited)", len(unlimited))
	}
}

func TestRoutesByTurnIDs(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	for i, id := range []string{"turn-a", "turn-b", "turn-c"} {
		r := sampleRoute(id, ts(10+i))
		r.Utterance = "utterance for " + id
		if _, err := s.InsertRoute(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.RoutesByTurnIDs(ctx, []string{"turn-a", "turn-c", "turn-does-not-exist"})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("RoutesByTurnIDs = %d rows, want 2: %v", len(rows), rowIDs(rows))
	}
	got := map[string]bool{}
	for _, r := range rows {
		got[r.TurnID] = true
	}
	if !got["turn-a"] || !got["turn-c"] {
		t.Fatalf("RoutesByTurnIDs = %v, want turn-a and turn-c", rowIDs(rows))
	}
}

func TestRoutesByTurnIDsEmpty(t *testing.T) {
	s, _ := openTemp(t)
	rows, err := s.RoutesByTurnIDs(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("RoutesByTurnIDs(nil) = %v, want none", rows)
	}
}

func TestIntentAnsweredRecent(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	for i, at := range []time.Time{ts(8), ts(9), ts(10)} {
		r := sampleRoute("learned"+string(rune('a'+i)), at)
		r.Intent = "learned.foo"
		if _, err := s.InsertRoute(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	other := sampleRoute("other", ts(11))
	other.Intent = "schedule.on_date"
	if _, err := s.InsertRoute(ctx, other); err != nil {
		t.Fatal(err)
	}
	rows, err := s.IntentAnswered(ctx, "learned.foo", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("IntentAnswered limit=2 returned %d rows", len(rows))
	}
	for _, r := range rows {
		if r.Intent != "learned.foo" {
			t.Fatalf("wrong intent in results: %+v", r)
		}
	}
}

func TestPruneRoutesBoundary(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if _, err := s.InsertRoute(ctx, sampleRoute("old", ts(8))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertRoute(ctx, sampleRoute("boundary", ts(10))); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertRoute(ctx, sampleRoute("new", ts(12))); err != nil {
		t.Fatal(err)
	}
	n, err := s.PruneRoutes(ctx, ts(10))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("PruneRoutes(before=ts(10)) removed %d, want 1 (only strictly-older)", n)
	}
	rows, _ := s.ListRoutes(ctx, ts(0), 0)
	if len(rows) != 2 {
		t.Fatalf("after prune: %v", rowIDs(rows))
	}
	for _, r := range rows {
		if r.TurnID == "old" {
			t.Fatal("boundary row (exactly at 'before') should have been pruned")
		}
	}
}

// TestRouterMigrationAppliesOnUpgrade proves the new migration applies
// cleanly to a database that already exists at the previous migration head,
// not only to a brand-new database (which TestRouteRowRoundTrip already
// exercises via openTemp -> Open, which always applies every migration).
func TestRouterMigrationAppliesOnUpgrade(t *testing.T) {
	s, path := openTemp(t)
	ctx := context.Background()
	// Simulate pre-existing data at the previous head: a message row that
	// the new indexes (messages_sent_at, messages_sender) must cover without
	// needing to be told about it.
	if err := s.Upsert(ctx, &Message{Meta: meta("m1", true), From: "dana@x.com", SentAt: ts(7)}); err != nil {
		t.Fatal(err)
	}
	s.Close()

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen after router migration: %v", err)
	}
	defer s2.Close()
	if _, err := s2.InsertRoute(ctx, sampleRoute("post-upgrade", ts(10))); err != nil {
		t.Fatalf("route_log unusable after upgrade: %v", err)
	}
	if err := s2.SetIntentState(ctx, "learned.x", true, "manual", ts(10)); err != nil {
		t.Fatalf("intent_state unusable after upgrade: %v", err)
	}
	msgs, err := s2.LatestMessages(ctx, 10)
	if err != nil || len(msgs) != 1 {
		t.Fatalf("pre-existing data survived migration: %v %v", msgs, err)
	}
}

// Slice W, D6 (migration 0015): route_log.class.
func TestRouteClassDefaultsToCompanyAndRoundTrips(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	unset := sampleRoute("unset", ts(10))
	general := sampleRoute("general", ts(11))
	general.Class = RouteClassGeneral
	bogus := sampleRoute("bogus", ts(12))
	bogus.Class = "chitchat" // anything but "general" is stored as company
	for _, r := range []RouteRow{unset, general, bogus} {
		if _, err := s.InsertRoute(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	// A row written without the column at all (the shape of every row that
	// existed before 0015) reads back as company.
	if _, err := s.db.ExecContext(ctx, `INSERT INTO route_log (turn_id, at, channel, utterance, tiers_attempted, total_ms, outcome) VALUES ('legacy', ?, 'voice', 'hi', '[]', 0, 'answered')`, ts(13).UnixNano()); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListRoutes(ctx, ts(0), 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, r := range rows {
		got[r.TurnID] = r.Class
	}
	want := map[string]string{"unset": "company", "general": "general", "bogus": "company", "legacy": "company"}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("class[%s] = %q, want %q", id, got[id], w)
		}
	}
}

func TestQuickOnlyRoutesExcludesGeneral(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	company := sampleRoute("company1", ts(10))
	company.Owner, company.QuickOnly = "main", true
	general := sampleRoute("general1", ts(11))
	general.Owner, general.QuickOnly, general.Class = "main", true, RouteClassGeneral
	for _, r := range []RouteRow{company, general} {
		if _, err := s.InsertRoute(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.QuickOnlyRoutes(ctx, ts(0), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].TurnID != "company1" {
		t.Fatalf("QuickOnlyRoutes = %v, want [company1]: a general turn is never promotion input", rowIDs(rows))
	}
}
