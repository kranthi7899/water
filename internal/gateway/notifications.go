package gateway

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"water/internal/store"
)

// Native notifications (docs/slices/V.md §7.4 V-notify). These two routes
// are for the native client's notifier only: they are deliberately absent
// from the web UI's api.js and from the Swift water:// scheme handler's
// WorkspaceAllowlist, so the page can never list or mark notifications
// (TestUIOnlyCallsAllowlistedRoutes pins that). Both sit behind d.auth like
// every other client route.

const (
	defaultNotificationListLimit = 20
	maxNotificationListLimit     = 100
)

// notificationView is one notification as GET /v1/notifications returns
// it. title and body are built by needsyou from the record (a decision
// card's lead can quote an email subject), so they are untrusted text:
// render them as plain strings only.
type notificationView struct {
	ID          string     `json:"id"`
	RecordType  string     `json:"record_type"` // decision|approval
	RecordID    string     `json:"record_id"`
	Title       string     `json:"title"`
	Body        string     `json:"body"`
	CreatedAt   time.Time  `json:"created_at"`
	DeliveredAt *time.Time `json:"delivered_at"`
}

func viewNotification(n store.Notification) notificationView {
	return notificationView{
		ID: n.ID, RecordType: n.RecordType, RecordID: n.RecordID, Title: n.Title, Body: n.Body,
		CreatedAt: n.CreatedAt, DeliveredAt: n.DeliveredAt,
	}
}

// handleListNotifications serves GET /v1/notifications?undelivered=1&limit=N:
// notifications not yet marked delivered, oldest first, at most limit
// (default 20, max 100). Only the undelivered listing exists; undelivered
// may be omitted, but any value other than 1 is a 400 rather than a
// silently different list.
func (d *Daemon) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if u, ok := q["undelivered"]; ok && (len(u) != 1 || u[0] != "1") {
		http.Error(w, "only undelivered=1 is supported", http.StatusBadRequest)
		return
	}
	limit := defaultNotificationListLimit
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n <= 0 {
			http.Error(w, "limit must be a positive integer", http.StatusBadRequest)
			return
		}
		limit = min(n, maxNotificationListLimit)
	}
	rows, err := d.cfg.Store.ListUndeliveredNotifications(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	out := make([]notificationView, 0, len(rows))
	for _, n := range rows {
		out = append(out, viewNotification(n))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleMarkNotificationDelivered serves POST /v1/notifications/{id}/delivered:
// the native client calls it after the banner was accepted by the OS. It is
// idempotent (a second call answers 200 with the first delivery time) and
// answers 404 for an id that was never recorded.
func (d *Daemon) handleMarkNotificationDelivered(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id := r.PathValue("id")
	if _, err := d.cfg.Store.GetNotification(ctx, id); errors.Is(err, store.ErrNotFound) {
		http.Error(w, "no notification "+id, http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := d.cfg.Store.MarkNotificationDelivered(ctx, id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	n, err := d.cfg.Store.GetNotification(ctx, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, viewNotification(n))
}
