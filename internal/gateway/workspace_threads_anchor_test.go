package gateway

import (
	"context"
	"net/http"
	"testing"

	"water/internal/store"
	"water/internal/workspaces"
)

// TestNewAnchorTypesBuildLabelledUntaintedSnapshots is docs/slices/UI.md
// Phase 3d: project/workspace/idea anchors each resolve to a code-built
// snapshot and an anchor_label, and (unlike approval/meeting, always
// untrusted) are never tainted — they're the CEO's own configured data or
// CEO-authored capture, not synced from an external connector.
func TestNewAnchorTypesBuildLabelledUntaintedSnapshots(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if err := h.st.Upsert(ctx, &store.Project{
		Meta: store.Meta{Source: "seed", SourceID: "meridian-renewal"}, Name: "Meridian renewal",
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.st.Upsert(ctx, &store.Workspace{
		Meta: store.Meta{Source: workspaces.SpecSource, SourceID: "ws_water"}, Name: "Water", Template: "project", PrimarySource: "linear_team:WAT",
	}); err != nil {
		t.Fatal(err)
	}
	idea, err := h.st.CreateIdea(ctx, store.Idea{Title: "A pricing experiment", Gist: "try usage-based tiers", Stage: "raw"})
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		anchorType, anchorID, wantLabel string
	}{
		{anchorProject, "meridian-renewal", "Project: Meridian renewal"},
		{anchorWorkspace, "ws_water", "Workspace: Water"},
		{anchorIdea, idea.ID, "Idea: A pricing experiment"},
	} {
		t.Run(tc.anchorType, func(t *testing.T) {
			var th threadAnchorResponse
			body := `{"anchor_type":"` + tc.anchorType + `","anchor_id":"` + tc.anchorID + `"}`
			decodeInto(t, do(t, h.srv.URL, "POST", "/v1/threads/anchor", body, h.token), http.StatusOK, &th)
			if th.Thread.AnchorType != tc.anchorType || th.Thread.AnchorID != tc.anchorID {
				t.Fatalf("thread = %+v", th.Thread)
			}
			if th.Thread.AnchorLabel != tc.wantLabel {
				t.Fatalf("anchor_label = %q, want %q", th.Thread.AnchorLabel, tc.wantLabel)
			}
			if th.Thread.AnchorUntrusted {
				t.Fatalf("anchor_untrusted = true, want false for a %s anchor (CEO's own data, not external)", tc.anchorType)
			}
			if th.Thread.AnchorContext == "" {
				t.Fatal("anchor_context is empty, want a code-built snapshot")
			}

			// Fetching the thread again returns the same label and
			// snapshot, unaffected by anything else in the store.
			var detail threadDetail
			decodeInto(t, do(t, h.srv.URL, "GET", "/v1/threads/"+th.Thread.ID, "", h.token), http.StatusOK, &detail)
			if detail.Thread.AnchorLabel != tc.wantLabel {
				t.Fatalf("GET thread anchor_label = %q, want %q", detail.Thread.AnchorLabel, tc.wantLabel)
			}
		})
	}
}

// TestNewAnchorTypes404OnUnknownID: an anchor id naming no record 404s,
// exactly like the existing anchor types.
func TestNewAnchorTypes404OnUnknownID(t *testing.T) {
	h := newHarness(t)
	for _, at := range []string{anchorProject, anchorWorkspace, anchorIdea} {
		body := `{"anchor_type":"` + at + `","anchor_id":"does-not-exist"}`
		resp := do(t, h.srv.URL, "POST", "/v1/threads/anchor", body, h.token)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("%s: status = %d, want 404", at, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// TestUnanchoredThreadLabelIsUnanchored: a free-standing thread (no
// anchor_type/anchor_id at all) reads "Unanchored", never the old "free"
// wording, and its stored anchor_type is genuinely "" — not a literal
// "free" value.
func TestUnanchoredThreadLabelIsUnanchored(t *testing.T) {
	h := newHarness(t)
	var created threadView
	decodeInto(t, do(t, h.srv.URL, "POST", "/v1/threads", `{"title":"loose ends"}`, h.token), http.StatusCreated, &created)
	if created.AnchorType != "" {
		t.Fatalf("anchor_type = %q, want empty for an unanchored thread", created.AnchorType)
	}
	if created.AnchorLabel != "Unanchored" {
		t.Fatalf("anchor_label = %q, want %q", created.AnchorLabel, "Unanchored")
	}

	var list []threadView
	decodeInto(t, do(t, h.srv.URL, "GET", "/v1/threads", "", h.token), http.StatusOK, &list)
	found := false
	for _, th := range list {
		if th.ID == created.ID {
			found = true
			if th.AnchorLabel != "Unanchored" {
				t.Fatalf("list anchor_label = %q, want %q", th.AnchorLabel, "Unanchored")
			}
		}
	}
	if !found {
		t.Fatal("created thread not present in the list")
	}
}
