package gateway

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"water/internal/approvals"
	"water/internal/audit"
	"water/internal/decisions"
	"water/internal/needsyou"
	"water/internal/store"
)

type cannedCards []*decisions.Card

func (c cannedCards) Run(context.Context, time.Time) ([]*decisions.Card, error) { return c, nil }

func listNotifications(t *testing.T, h *harness, query string) []notificationView {
	t.Helper()
	var out []notificationView
	decodeInto(t, do(t, h.srv.URL, "GET", "/v1/notifications"+query, "", h.token), http.StatusOK, &out)
	return out
}

func TestNotificationRoutesRequireAClientToken(t *testing.T) {
	h := newHarness(t)
	for _, rt := range []struct{ method, path string }{
		{"GET", "/v1/notifications?undelivered=1"},
		{"POST", "/v1/notifications/ntf_x/delivered"},
	} {
		for _, tok := range []string{"", "bogus"} {
			if got := statusOf(do(t, h.srv.URL, rt.method, rt.path, "", tok)); got != http.StatusUnauthorized {
				t.Errorf("%s %s with token %q: status %d, want 401", rt.method, rt.path, tok, got)
			}
		}
	}
}

// TestNotificationsListOnlyUndeliveredAndDeliveredIsIdempotent: the list
// holds only undelivered rows, oldest first and capped by limit; marking
// delivered removes a row, is idempotent (same delivered_at twice), and an
// unknown id is 404.
func TestNotificationsListOnlyUndeliveredAndDeliveredIsIdempotent(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	for i, id := range []string{"ntf_a", "ntf_b", "ntf_c"} {
		if _, err := h.st.InsertNotificationIfNew(ctx, store.Notification{
			ID: id, RecordType: "decision", RecordID: "card-" + id, Title: "<b>Lead</b>", Body: "Severity 3",
			CreatedAt: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.st.MarkNotificationDelivered(ctx, "ntf_b"); err != nil {
		t.Fatal(err)
	}

	got := listNotifications(t, h, "?undelivered=1")
	if len(got) != 2 || got[0].ID != "ntf_a" || got[1].ID != "ntf_c" {
		t.Fatalf("undelivered = %+v, want [ntf_a ntf_c]", got)
	}
	if got[0].RecordType != "decision" || got[0].RecordID != "card-ntf_a" || got[0].Title != "<b>Lead</b>" || got[0].DeliveredAt != nil {
		t.Fatalf("row = %+v", got[0])
	}
	if got := listNotifications(t, h, "?undelivered=1&limit=1"); len(got) != 1 || got[0].ID != "ntf_a" {
		t.Fatalf("limit=1 = %+v, want [ntf_a]", got)
	}
	if got := listNotifications(t, h, ""); len(got) != 2 {
		t.Fatalf("no query = %+v, want the undelivered list", got)
	}
	for _, bad := range []string{"?undelivered=0", "?undelivered=true", "?undelivered=1&undelivered=1", "?limit=0", "?limit=x", "?limit=-1"} {
		if got := statusOf(do(t, h.srv.URL, "GET", "/v1/notifications"+bad, "", h.token)); got != http.StatusBadRequest {
			t.Errorf("GET %s: status %d, want 400", bad, got)
		}
	}

	var first, second notificationView
	decodeInto(t, do(t, h.srv.URL, "POST", "/v1/notifications/ntf_a/delivered", "", h.token), http.StatusOK, &first)
	if first.ID != "ntf_a" || first.DeliveredAt == nil {
		t.Fatalf("first delivered = %+v", first)
	}
	decodeInto(t, do(t, h.srv.URL, "POST", "/v1/notifications/ntf_a/delivered", "", h.token), http.StatusOK, &second)
	if second.DeliveredAt == nil || !second.DeliveredAt.Equal(*first.DeliveredAt) {
		t.Fatalf("second delivered = %+v, want the first delivery time %v kept", second, first.DeliveredAt)
	}
	if got := listNotifications(t, h, "?undelivered=1"); len(got) != 1 || got[0].ID != "ntf_c" {
		t.Fatalf("after delivery = %+v, want [ntf_c]", got)
	}
	if got := statusOf(do(t, h.srv.URL, "POST", "/v1/notifications/ntf_missing/delivered", "", h.token)); got != http.StatusNotFound {
		t.Fatalf("unknown id: status %d, want 404", got)
	}
}

// TestNotificationAcceptanceOneBannerEverAcrossRestart is §7.4 V-notify's
// acceptance on the daemon side: a fixture decision at the threshold
// yields exactly one undelivered notification; once the native client
// marks it delivered, a restarted needs-you service (a fresh store handle
// and a fresh Service, so no in-memory state survives) ticking the same
// card never lists it again.
func TestNotificationAcceptanceOneBannerEverAcrossRestart(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	card := cannedCards{{ID: "card-0123456789abcdef", Severity: 3, Lead: "Approve the Q3 budget?"}}
	below := cannedCards{{ID: "card-low", Severity: 1, Lead: "minor"}}

	svc := needsyou.NewService(append(card, below...), h.q, h.st, 2, time.Minute)
	for i := 0; i < 2; i++ {
		if err := svc.Tick(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	got := listNotifications(t, h, "?undelivered=1")
	if len(got) != 1 || got[0].RecordType != "decision" || got[0].RecordID != "card-0123456789abcdef" {
		t.Fatalf("after two ticks = %+v, want exactly the one decision", got)
	}
	if statusOf(do(t, h.srv.URL, "POST", "/v1/notifications/"+got[0].ID+"/delivered", "", h.token)) != http.StatusOK {
		t.Fatal("mark delivered failed")
	}

	// "Restart": a new store handle on the same file and a new Service.
	st2, err := store.Open(filepath.Join(h.dir, "water.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	log2, err := audit.Open(filepath.Join(t.TempDir(), "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer log2.Close()
	svc2 := needsyou.NewService(card, approvals.NewQueue(st2, log2), st2, 2, time.Minute)
	if err := svc2.Tick(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if got := listNotifications(t, h, "?undelivered=1"); len(got) != 0 {
		t.Fatalf("after restart = %+v, want nothing: one banner, ever", got)
	}
}
