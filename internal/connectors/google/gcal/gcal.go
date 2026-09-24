// Package gcal is the read-only Google Calendar connector: it lists events
// in a time range through gapi and normalizes them into store.Event.
package gcal

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"water/internal/connectors"
	"water/internal/connectors/google/gapi"
	"water/internal/gate/permit"
	"water/internal/store"
	"water/internal/twins"
)

const (
	defaultMax = 250
	maxMax     = 250
	// maxPages bounds how many pages Invoke will follow, independent of the
	// caller's max, so a misbehaving server cannot loop it forever.
	maxPages = 40
	// descriptionCap limits how much of an event's description (written by
	// someone else) is surfaced in the tool output.
	descriptionCap = 280
)

// Event is the compact shape Invoke returns and Normalize consumes.
type Event struct {
	ID          string   `json:"id"`
	CalendarID  string   `json:"calendar_id"`
	Title       string   `json:"title"`
	Start       string   `json:"start"`
	End         string   `json:"end,omitempty"`
	Location    string   `json:"location,omitempty"`
	Attendees   []string `json:"attendees,omitempty"`
	Organizer   string   `json:"organizer,omitempty"`
	Status      string   `json:"status,omitempty"`
	HTMLLink    string   `json:"html_link,omitempty"`
	Description string   `json:"description,omitempty"`
}

// wireEvent is Google Calendar's events resource, trimmed to what gcal uses.
type wireEvent struct {
	ID          string         `json:"id"`
	Summary     string         `json:"summary"`
	Description string         `json:"description"`
	Location    string         `json:"location"`
	Status      string         `json:"status"`
	HTMLLink    string         `json:"htmlLink"`
	Start       wireWhen       `json:"start"`
	End         wireWhen       `json:"end"`
	Attendees   []wireAttendee `json:"attendees"`
	Organizer   *wireAttendee  `json:"organizer"`
}

type wireWhen struct {
	Date     string `json:"date"`
	DateTime string `json:"dateTime"`
}

type wireAttendee struct {
	Email string `json:"email"`
}

// Calendar is the gcal connector. opts is nil in production and set in
// tests to point at an httptest server.
type Calendar struct{ opts *gapi.Options }

// New builds the production connector.
func New() *Calendar { return &Calendar{} }

// NewWithOptions builds a connector against overridden gapi options, for
// tests.
func NewWithOptions(o *gapi.Options) *Calendar { return &Calendar{opts: o} }

func (*Calendar) Name() string { return "gcal" }

func (*Calendar) Credential() (string, string) { return gapi.Service, gapi.DefaultAccount }

func (*Calendar) Functions() []connectors.Function {
	return []connectors.Function{
		{
			Name:        "list_events",
			Description: "List calendar events in a time range.",
			Level:       twins.R,
			Risk:        connectors.RiskLow,
			// Titles, locations, descriptions and attendees come from
			// whoever created or was invited to the event, not the CEO.
			External: true,
			Schema: connectors.Schema{
				Properties: map[string]connectors.Property{
					"time_min":    {Type: "string", Description: "RFC 3339 start of the range (required)"},
					"time_max":    {Type: "string", Description: "RFC 3339 end of the range (required)"},
					"calendar_id": {Type: "string", Description: `calendar id, default "primary"`},
					"max":         {Type: "integer", Description: "maximum events to return, capped at 250"},
				},
				Required: []string{"time_min", "time_max"},
			},
		},
	}
}

func (c *Calendar) Invoke(ctx context.Context, p permit.Permit) (json.RawMessage, error) {
	v, err := p.Open()
	if err != nil {
		return nil, err
	}
	if v.Function != "list_events" {
		return nil, fmt.Errorf("gcal: unknown function %q", v.Function)
	}
	timeMin := gapi.ArgString(v.Args, "time_min")
	timeMax := gapi.ArgString(v.Args, "time_max")
	if timeMin == "" || timeMax == "" {
		return nil, fmt.Errorf("gcal: time_min and time_max are required")
	}
	if _, err := time.Parse(time.RFC3339, timeMin); err != nil {
		return nil, fmt.Errorf("gcal: time_min must be RFC 3339: %w", err)
	}
	if _, err := time.Parse(time.RFC3339, timeMax); err != nil {
		return nil, fmt.Errorf("gcal: time_max must be RFC 3339: %w", err)
	}
	calendarID := gapi.ArgString(v.Args, "calendar_id")
	if calendarID == "" {
		calendarID = "primary"
	}
	max, err := gapi.ArgInt(v.Args, "max", defaultMax, 1, maxMax)
	if err != nil {
		return nil, fmt.Errorf("gcal: %w", err)
	}

	cl, err := gapi.FromSecret(v.Credential, c.opts)
	if err != nil {
		return nil, err
	}

	endpoint := gapi.CalendarBase + "/calendars/" + url.PathEscape(calendarID) + "/events"
	events := make([]Event, 0, max)
	pageToken := ""
	for page := 0; page < maxPages; page++ {
		q := url.Values{
			"singleEvents": {"true"},
			"orderBy":      {"startTime"},
			"timeMin":      {timeMin},
			"timeMax":      {timeMax},
			"maxResults":   {strconv.Itoa(max)},
		}
		if pageToken != "" {
			q.Set("pageToken", pageToken)
		}
		var resp struct {
			Items         []wireEvent `json:"items"`
			NextPageToken string      `json:"nextPageToken"`
		}
		if err := cl.GetJSON(ctx, endpoint, q, &resp); err != nil {
			return nil, err
		}
		for _, w := range resp.Items {
			events = append(events, toEvent(w, calendarID))
			if len(events) >= max {
				break
			}
		}
		if len(events) >= max || resp.NextPageToken == "" {
			break
		}
		pageToken = resp.NextPageToken
	}

	return json.Marshal(events)
}

func toEvent(w wireEvent, calendarID string) Event {
	var attendees []string
	for _, a := range w.Attendees {
		if a.Email != "" {
			attendees = append(attendees, a.Email)
		}
	}
	organizer := ""
	if w.Organizer != nil {
		organizer = w.Organizer.Email
	}
	start := w.Start.DateTime
	if start == "" {
		start = w.Start.Date
	}
	end := w.End.DateTime
	if end == "" {
		end = w.End.Date
	}
	return Event{
		ID:          w.ID,
		CalendarID:  calendarID,
		Title:       w.Summary,
		Start:       start,
		End:         end,
		Location:    w.Location,
		Attendees:   attendees,
		Organizer:   organizer,
		Status:      w.Status,
		HTMLLink:    w.HTMLLink,
		Description: capText(w.Description, descriptionCap),
	}
}

func capText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// eventTime parses either an all-day date or a timed dateTime, in UTC.
func eventTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UTC()
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func (c *Calendar) Normalize(fn string, raw json.RawMessage) ([]store.Record, error) {
	if fn != "list_events" {
		return nil, nil
	}
	var events []Event
	if err := json.Unmarshal(raw, &events); err != nil {
		return nil, err
	}
	out := make([]store.Record, 0, len(events))
	for _, e := range events {
		out = append(out, &store.Event{
			Meta:      store.Meta{Source: c.Name(), SourceID: e.CalendarID + ":" + e.ID, External: true},
			Title:     e.Title,
			StartAt:   eventTime(e.Start),
			EndAt:     eventTime(e.End),
			Location:  e.Location,
			Attendees: e.Attendees,
			Organizer: e.Organizer,
			Status:    e.Status,
		})
	}
	return out, nil
}
