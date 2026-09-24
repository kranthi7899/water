// Package meetings is live-meeting session state (Slice M): a session's
// lifecycle and its transcript segments. Segments are text only, recognized
// on-device by the client; audio never reaches this package. Every segment,
// on either channel, is untrusted external content: it can be quoted,
// searched and summarized, never obeyed.
package meetings

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"water/internal/store"
)

type Channel string

const (
	Mic    Channel = "mic"    // the CEO's microphone
	System Channel = "system" // system audio: everyone else in the meeting
)

// MaxSegmentText bounds one segment's text; a recognizer emits a sentence or
// two at a time, so anything larger is not a transcript segment.
const MaxSegmentText = 8 << 10

var (
	ErrNotFound   = store.ErrNotFound
	ErrEnded      = store.ErrMeetingEnded
	ErrBadSegment = errors.New("invalid segment")
)

type Session struct {
	ID        string
	StartedAt time.Time
	EndedAt   *time.Time
	EventID   string // store.Event.SourceID, if started from a calendar event
}

// Segment is one recognized utterance. External is always true: it is
// set by this package on every segment it returns, whatever the channel.
type Segment struct {
	SessionID string
	At        time.Time
	Channel   Channel
	Text      string
	External  bool
}

type Manager struct {
	st  *store.Store
	now func() time.Time

	// cueMu/cueState back Cues' rate limiting (section 6): last-shown batch
	// per session, in memory only, lost on daemon restart like every other
	// gate rate window in this repo.
	cueMu    sync.Mutex
	cueState map[string]cueState
}

func New(st *store.Store) *Manager {
	return &Manager{st: st, now: time.Now, cueState: map[string]cueState{}}
}

func (m *Manager) Start(ctx context.Context, eventID string) (Session, error) {
	var b [12]byte
	_, _ = rand.Read(b[:])
	s := Session{ID: "mtg_" + hex.EncodeToString(b[:]), StartedAt: m.now().UTC(), EventID: strings.TrimSpace(eventID)}
	if err := m.st.InsertMeetingSession(ctx, store.MeetingSessionRow{ID: s.ID, StartedAt: s.StartedAt, EventID: s.EventID}); err != nil {
		return Session{}, err
	}
	return s, nil
}

func (m *Manager) Get(ctx context.Context, id string) (Session, error) {
	r, err := m.st.GetMeetingSession(ctx, id)
	if err != nil {
		return Session{}, err
	}
	return Session{ID: r.ID, StartedAt: r.StartedAt, EndedAt: r.EndedAt, EventID: r.EventID}, nil
}

// Stop ends a session. Stopping an already-ended session is not an error:
// it returns the session with its original end time.
func (m *Manager) Stop(ctx context.Context, id string) (Session, error) {
	if _, err := m.Get(ctx, id); err != nil {
		return Session{}, err
	}
	if _, err := m.st.EndMeetingSession(ctx, id, m.now().UTC()); err != nil {
		return Session{}, err
	}
	return m.Get(ctx, id)
}

// MaxSegmentSkew is how far past the daemon's clock a segment's At may be
// before it is clamped to now.
const MaxSegmentSkew = 5 * time.Second

// AddSegment appends one segment to a live session.
//
// seg.At is when the speech began (optional; zero means now). It orders the
// transcript and is clamped to [session start, now + MaxSegmentSkew], so a
// bad client clock cannot pin a line into the rolling windows or push it
// ahead of the meeting. Help's and cues' rolling windows select segments by
// when the daemon received them, not by At, so a long utterance stamped with
// its start (posted tens of seconds later) is still "recent" when it lands.
func (m *Manager) AddSegment(ctx context.Context, sessionID string, seg Segment) error {
	text := strings.TrimSpace(seg.Text)
	if seg.Channel != Mic && seg.Channel != System {
		return errors.Join(ErrBadSegment, errors.New(`channel must be "mic" or "system"`))
	}
	if text == "" || len(text) > MaxSegmentText {
		return errors.Join(ErrBadSegment, errors.New("text must be non-empty and at most 8 KiB"))
	}
	s, err := m.Get(ctx, sessionID)
	if err != nil {
		return err
	}
	if s.EndedAt != nil {
		return ErrEnded
	}
	now := m.now()
	at := seg.At
	switch {
	case at.IsZero():
		at = now
	case at.After(now.Add(MaxSegmentSkew)):
		at = now
	case at.Before(s.StartedAt):
		at = s.StartedAt
	}
	return m.st.InsertMeetingSegment(ctx, store.MeetingSegmentRow{SessionID: sessionID, At: at.UTC(), ReceivedAt: now.UTC(), Channel: string(seg.Channel), Text: text})
}

// Segments returns a session's whole transcript, oldest first.
func (m *Manager) Segments(ctx context.Context, sessionID string) ([]Segment, error) {
	return m.SegmentsSince(ctx, sessionID, time.Time{})
}

// SegmentsSince returns the segments at or after since, oldest first: the
// rolling window on-demand help reads ("the last few minutes").
func (m *Manager) SegmentsSince(ctx context.Context, sessionID string, since time.Time) ([]Segment, error) {
	if _, err := m.Get(ctx, sessionID); err != nil {
		return nil, err
	}
	rows, err := m.st.ListMeetingSegments(ctx, sessionID, since)
	if err != nil {
		return nil, err
	}
	out := make([]Segment, len(rows))
	for i, r := range rows {
		out[i] = Segment{SessionID: r.SessionID, At: r.At, Channel: Channel(r.Channel), Text: r.Text, External: true}
	}
	return out, nil
}
