package gateway

import (
	"context"
	"net/http"
	"testing"
	"time"

	"water/internal/store"
)

// Slice V-links (docs/slices/V.md §7.4, D6): the deterministic links the
// workspace handlers write. newStageTestDaemon's one card comes from
// gmail:msg-1, sent by dana@example.com.

func seedRosterPerson(t *testing.T, st *store.Store, id, email string) {
	t.Helper()
	if err := st.Upsert(context.Background(), &store.Person{Meta: store.Meta{Source: "seed", SourceID: id}, Name: id, Identities: `{"email":"` + email + `"}`}); err != nil {
		t.Fatal(err)
	}
}

func decisionLinks(t *testing.T, st *store.Store, cardID string) []store.Link {
	t.Helper()
	links, err := st.LinksOf(context.Background(), "decision", cardID)
	if err != nil {
		t.Fatal(err)
	}
	return links
}

const stagePayload = `{"payload":{"to":["dana@example.com"],"subject":"Re: Speaking invite","body":"Yes, happy to."}}`

// TestDecisionWritersLinkTheResolvedSender: stage, dismiss and anchoring
// each write decision -> involves -> dana on their own, exactly once, even
// when repeated; with no roster match none of them writes anything.
func TestDecisionWritersLinkTheResolvedSender(t *testing.T) {
	writers := map[string]func(t *testing.T, srvURL, tok, id string){
		"stage": func(t *testing.T, srvURL, tok, id string) {
			decodeInto(t, do(t, srvURL, "POST", "/v1/decisions/"+id+"/stage", stagePayload, tok), http.StatusOK, nil)
		},
		"dismiss": func(t *testing.T, srvURL, tok, id string) {
			decodeInto(t, do(t, srvURL, "POST", "/v1/decisions/"+id+"/dismiss", `{"reason":"not now"}`, tok), http.StatusOK, nil)
		},
		"anchor": func(t *testing.T, srvURL, tok, id string) {
			decodeInto(t, do(t, srvURL, "POST", "/v1/threads/anchor", `{"anchor_type":"decision","anchor_id":"`+id+`"}`, tok), http.StatusOK, nil)
		},
	}
	for name, write := range writers {
		t.Run(name+"/resolved", func(t *testing.T) {
			srv, tok, _, st := newStageTestDaemon(t, decisionType("[gmail.send_message]"), "inbound")
			seedRosterPerson(t, st, "dana", "dana@example.com")
			cards := getDecisions(t, srv, tok)
			if len(cards) != 1 {
				t.Fatalf("cards = %d", len(cards))
			}
			id := cards[0].ID
			if got := decisionLinks(t, st, id); len(got) != 0 {
				t.Fatalf("links before %s = %+v, want none (listing writes nothing)", name, got)
			}
			write(t, srv.URL, tok, id)
			write(t, srv.URL, tok, id) // a repeat: already_staged, re-dismiss, existing thread
			want := store.Link{Kind: store.LinkInvolves, FromType: "decision", FromID: id, ToType: "person", ToID: "dana"}
			var involves []store.Link
			for _, l := range decisionLinks(t, st, id) {
				if l.Kind == store.LinkInvolves {
					involves = append(involves, l)
				}
			}
			if len(involves) != 1 || involves[0] != want {
				t.Fatalf("involves after %s = %+v, want exactly %+v", name, involves, want)
			}
		})
		t.Run(name+"/unresolved", func(t *testing.T) {
			srv, tok, _, st := newStageTestDaemon(t, decisionType("[gmail.send_message]"), "inbound")
			seedRosterPerson(t, st, "sam", "sam@example.com") // someone, but not the sender
			cards := getDecisions(t, srv, tok)
			if len(cards) != 1 {
				t.Fatalf("cards = %d", len(cards))
			}
			write(t, srv.URL, tok, cards[0].ID)
			for _, l := range decisionLinks(t, st, cards[0].ID) {
				if l.Kind == store.LinkInvolves {
					t.Fatalf("an unresolved sender wrote %+v", l)
				}
			}
		})
	}
}

// TestRestageAfterDenialAddsNoDuplicateLink: a second, fresh staging of the
// same card (after the first envelope was denied) re-runs the writer.
func TestRestageAfterDenialAddsNoDuplicateLink(t *testing.T) {
	srv, tok, _, st := newStageTestDaemon(t, decisionType("[gmail.send_message]"), "inbound")
	seedRosterPerson(t, st, "dana", "dana@example.com")
	id := getDecisions(t, srv, tok)[0].ID
	var staged decisionStageResponse
	decodeInto(t, do(t, srv.URL, "POST", "/v1/decisions/"+id+"/stage", stagePayload, tok), http.StatusOK, &staged)
	decodeInto(t, do(t, srv.URL, "POST", "/v1/approvals/"+staged.ApprovalID+"/decision", `{"payload_hash":"`+staged.Envelope.PayloadHash+`","reply":"no"}`, tok), http.StatusOK, nil)
	var fresh decisionStageResponse
	decodeInto(t, do(t, srv.URL, "POST", "/v1/decisions/"+id+"/stage", stagePayload, tok), http.StatusOK, &fresh)
	if fresh.Status != "queued" {
		t.Fatalf("restage = %+v", fresh)
	}
	if got := decisionLinks(t, st, id); len(got) != 1 {
		t.Fatalf("links after restage = %+v, want exactly one", got)
	}
}

// TestAnchoredThreadCopiesItsAnchorsLinks: a new thread inherits its
// anchor's involves and for_project edges (and nothing else); anchoring
// again returns the same thread and duplicates nothing.
func TestAnchoredThreadCopiesItsAnchorsLinks(t *testing.T) {
	t.Run("decision", func(t *testing.T) {
		srv, tok, _, st := newStageTestDaemon(t, decisionType("[gmail.send_message]"), "inbound")
		seedRosterPerson(t, st, "dana", "dana@example.com")
		id := getDecisions(t, srv, tok)[0].ID
		body := `{"anchor_type":"decision","anchor_id":"` + id + `"}`
		var th threadAnchorResponse
		decodeInto(t, do(t, srv.URL, "POST", "/v1/threads/anchor", body, tok), http.StatusOK, &th)
		decodeInto(t, do(t, srv.URL, "POST", "/v1/threads/anchor", body, tok), http.StatusOK, nil)
		links, err := st.LinksFrom(context.Background(), "thread", th.Thread.ID, store.LinkInvolves)
		if err != nil {
			t.Fatal(err)
		}
		want := store.Link{Kind: store.LinkInvolves, FromType: "thread", FromID: th.Thread.ID, ToType: "person", ToID: "dana"}
		if len(links) != 1 || links[0] != want {
			t.Fatalf("thread involves = %+v, want exactly %+v", links, want)
		}
	})

	t.Run("meeting", func(t *testing.T) {
		h := newHarness(t)
		ctx := context.Background()
		seedRosterPerson(t, h.st, "dana", "dana@example.com")
		now := time.Now().UTC()
		if err := h.st.Upsert(ctx, &store.Event{
			Meta: store.Meta{Source: "gcal", SourceID: "evt-links"}, Title: "Halcyon sync", StartAt: now, EndAt: now.Add(time.Hour),
			Attendees: []string{"dana@example.com", "stranger@elsewhere.com"},
		}); err != nil {
			t.Fatal(err)
		}
		s, err := h.d.meetings.Start(ctx, "evt-links")
		if err != nil {
			t.Fatal(err)
		}
		// A for_project edge on the meeting (written by hand here: nothing in
		// V derives one) is copied too; an in_workspace edge is not.
		for _, l := range []store.Link{
			{Kind: store.LinkForProject, FromType: "meeting", FromID: s.ID, ToType: "project", ToID: "halcyon-rollout"},
			{Kind: store.LinkInWorkspace, FromType: "meeting", FromID: s.ID, ToType: "workspace", ToID: "ws_1"},
		} {
			if err := h.st.AddLink(ctx, l); err != nil {
				t.Fatal(err)
			}
		}
		body := `{"anchor_type":"meeting","anchor_id":"` + s.ID + `"}`
		var th threadAnchorResponse
		decodeInto(t, do(t, h.srv.URL, "POST", "/v1/threads/anchor", body, h.token), http.StatusOK, &th)
		decodeInto(t, do(t, h.srv.URL, "POST", "/v1/threads/anchor", body, h.token), http.StatusOK, nil)

		links, err := h.st.LinksOf(ctx, "thread", th.Thread.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := map[store.Link]bool{
			{Kind: store.LinkAbout, FromType: "thread", FromID: th.Thread.ID, ToType: "meeting", ToID: s.ID}:                   true,
			{Kind: store.LinkInvolves, FromType: "thread", FromID: th.Thread.ID, ToType: "person", ToID: "dana"}:               true,
			{Kind: store.LinkForProject, FromType: "thread", FromID: th.Thread.ID, ToType: "project", ToID: "halcyon-rollout"}: true,
		}
		if len(links) != len(want) {
			t.Fatalf("thread links = %+v, want exactly %d", links, len(want))
		}
		for _, l := range links {
			if !want[l] {
				t.Fatalf("unexpected thread link %+v", l)
			}
		}
	})

	t.Run("anchor with no links copies nothing", func(t *testing.T) {
		h := newHarness(t)
		ctx := context.Background()
		s, err := h.d.meetings.Start(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		var th threadAnchorResponse
		decodeInto(t, do(t, h.srv.URL, "POST", "/v1/threads/anchor", `{"anchor_type":"meeting","anchor_id":"`+s.ID+`"}`, h.token), http.StatusOK, &th)
		links, err := h.st.LinksOf(ctx, "thread", th.Thread.ID)
		if err != nil || len(links) != 1 || links[0].Kind != store.LinkAbout {
			t.Fatalf("thread links = %+v, %v; want only the about edge", links, err)
		}
	})
}

// TestLinksAcceptanceOneMeetingOneDecision is §7.4 V-links' acceptance:
// after one fixture meeting and one fixture decision, LinksOf returns the
// expected person edges.
func TestLinksAcceptanceOneMeetingOneDecision(t *testing.T) {
	srv, tok, _, st := newStageTestDaemon(t, decisionType("[gmail.send_message]"), "inbound")
	ctx := context.Background()
	seedRosterPerson(t, st, "dana", "dana@example.com")
	seedRosterPerson(t, st, "sam", "sam@example.com")

	id := getDecisions(t, srv, tok)[0].ID
	decodeInto(t, do(t, srv.URL, "POST", "/v1/decisions/"+id+"/stage", stagePayload, tok), http.StatusOK, nil)

	now := time.Now().UTC()
	if err := st.Upsert(ctx, &store.Event{Meta: store.Meta{Source: "gcal", SourceID: "evt-acc"}, StartAt: now, EndAt: now.Add(time.Hour), Attendees: []string{"sam@example.com", "dana@example.com"}}); err != nil {
		t.Fatal(err)
	}
	var started struct {
		SessionID string `json:"session_id"`
	}
	decodeInto(t, do(t, srv.URL, "POST", "/v1/meetings/start", `{"event_id":"evt-acc"}`, tok), http.StatusOK, &started)

	people := func(typ, rid string) map[string]bool {
		t.Helper()
		out := map[string]bool{}
		links, err := st.LinksOf(ctx, typ, rid)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range links {
			if l.Kind == store.LinkInvolves && l.ToType == "person" {
				out[l.ToID] = true
			}
		}
		return out
	}
	if got := people("decision", id); len(got) != 1 || !got["dana"] {
		t.Fatalf("decision people = %v, want {dana}", got)
	}
	if got := people("meeting", started.SessionID); len(got) != 2 || !got["dana"] || !got["sam"] {
		t.Fatalf("meeting people = %v, want {dana, sam}", got)
	}
	// And the person side sees both records.
	back, err := st.LinksTo(ctx, "person", "dana", store.LinkInvolves)
	if err != nil || len(back) != 2 {
		t.Fatalf("dana's involves edges = %+v, %v; want 2", back, err)
	}
}
