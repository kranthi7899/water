package store

import (
	"context"
	"testing"
)

func TestCreateDraftRoundTrip(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	d, err := s.CreateDraft(ctx, Draft{Template: "reply", To: "dana@acme.com", Subject: "Re: renewal", Body: "Thanks for the note.", SourceCardID: "card-1", Provenance: "demo_seed", Simulated: true})
	if err != nil {
		t.Fatal(err)
	}
	if d.ID == "" {
		t.Fatal("expected a generated ID")
	}
	if d.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt should default to now")
	}

	got, err := s.GetDraft(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Template != "reply" || got.To != "dana@acme.com" || got.Subject != "Re: renewal" ||
		got.Body != "Thanks for the note." || got.SourceCardID != "card-1" || got.Provenance != "demo_seed" || !got.Simulated {
		t.Fatalf("got = %+v", got)
	}
}

// TestCreateDraftAcceptsThePeopleWorkspaceTemplates is docs/slices/UI.md
// Phase 5b: migration 0023 and draftTemplates both widen to accept
// team_message and pulse_check alongside the three Phase 3c template
// values, exercised here exactly like TestCreateDraftRoundTrip does for
// "reply". Phase 5c's own idea_proposal template (migration 0024, the
// Ideas workspace's "Propose" button) is exercised the same way.
func TestCreateDraftAcceptsThePeopleWorkspaceTemplates(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()

	for _, template := range []string{"team_message", "pulse_check", "idea_proposal"} {
		d, err := s.CreateDraft(ctx, Draft{Template: template, Subject: "s", Body: "b"})
		if err != nil {
			t.Fatalf("%s: %v", template, err)
		}
		got, err := s.GetDraft(ctx, d.ID)
		if err != nil {
			t.Fatalf("%s: %v", template, err)
		}
		if got.Template != template {
			t.Fatalf("%s: got template %q", template, got.Template)
		}
		if label := DraftTemplateLabel(template); label == "" {
			t.Fatalf("%s: DraftTemplateLabel returned empty", template)
		}
	}
}

func TestCreateDraftRejectsUnknownTemplate(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if _, err := s.CreateDraft(ctx, Draft{Template: "newsletter"}); err == nil {
		t.Fatal("expected an error for an unknown template, got nil")
	}
	all, err := s.ListDrafts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("a rejected draft should not persist, got %+v", all)
	}
}

func TestGetDraftUnknownIDIsErrNotFound(t *testing.T) {
	s, _ := openTemp(t)
	if _, err := s.GetDraft(context.Background(), "draft_missing"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestSaveDraftUpdatesAndBumpsUpdatedAt(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	d, err := s.CreateDraft(ctx, Draft{Template: "delegation", To: "a@b.com", Subject: "old", Body: "old body"})
	if err != nil {
		t.Fatal(err)
	}
	first := d.UpdatedAt

	got, err := s.SaveDraft(ctx, d.ID, "b@c.com", "new subject", "new body")
	if err != nil {
		t.Fatal(err)
	}
	if got.To != "b@c.com" || got.Subject != "new subject" || got.Body != "new body" {
		t.Fatalf("saved draft = %+v", got)
	}
	if got.UpdatedAt.Before(first) {
		t.Fatalf("UpdatedAt went backwards: %v -> %v", first, got.UpdatedAt)
	}
	// Template and SourceCardID are untouched by Save -- it only ever
	// updates to/subject/body/updated_at.
	if got.Template != "delegation" {
		t.Fatalf("template changed: %+v", got)
	}

	reGot, err := s.GetDraft(ctx, d.ID)
	if err != nil || reGot != got {
		t.Fatalf("GetDraft after SaveDraft = %+v, %v; want %+v", reGot, err, got)
	}
}

func TestSaveDraftAllowsEmptyFields(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	d, err := s.CreateDraft(ctx, Draft{Template: "reply", To: "a@b.com", Subject: "s", Body: "b"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SaveDraft(ctx, d.ID, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.To != "" || got.Subject != "" || got.Body != "" {
		t.Fatalf("got = %+v, want all cleared (a draft may be incomplete)", got)
	}
}

func TestSaveDraftUnknownIDIsErrNotFoundAndInsertsNothing(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	if _, err := s.SaveDraft(ctx, "draft_missing", "a@b.com", "s", "b"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	all, err := s.ListDrafts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("SaveDraft on an unknown id inserted a row: %+v", all)
	}
}

func TestListDraftsOrdersMostRecentlyUpdatedFirst(t *testing.T) {
	s, _ := openTemp(t)
	ctx := context.Background()
	a, err := s.CreateDraft(ctx, Draft{Template: "reply", To: "a@b.com"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateDraft(ctx, Draft{Template: "delegation", To: "c@d.com"})
	if err != nil {
		t.Fatal(err)
	}
	// Touch a again so it becomes the most recently updated.
	if _, err := s.SaveDraft(ctx, a.ID, "a2@b.com", "s", "body"); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListDrafts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != a.ID || list[1].ID != b.ID {
		t.Fatalf("list = %+v, want [%s, %s]", list, a.ID, b.ID)
	}
}
