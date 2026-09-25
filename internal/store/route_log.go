package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// RouteRow is one turn's routing decision (Slice R's nervous system),
// written once per turn from the front door's deferred cleanup path.
type RouteRow struct {
	ID                    int64
	TurnID                string
	ClientTurnID          string
	At                    time.Time
	Channel               string
	Utterance             string
	TiersAttempted        []string
	Owner                 string
	AnsweredBy            string
	Intent                string
	IntentKind            string
	IntentOrigin          string
	Slots                 map[string]string
	EscalationReason      string
	LatencyMS             map[string]int64
	TotalMS               int64
	Outcome               string
	Warnings              []string
	Voice                 bool
	Partials              int
	FirstPartialLeadMS    *int64
	Speculation           map[string]any
	SpeculationModelCalls int
	AckMS                 *int64
	FirstSentenceMS       *int64
	ToolsUsed             []string
	ToolsAttributed       bool
	QuickOnly             bool
	ToolSignature         string
	Action                map[string]string
	PossibleMiss          bool
	Confirmed             bool
}

func marshalJSON(v any) (string, error) {
	if v == nil {
		return "", nil
	}
	b, err := json.Marshal(v)
	return string(b), err
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// InsertRoute writes one route_log row. r.At defaults to now when zero.
func (s *Store) InsertRoute(ctx context.Context, r RouteRow) (int64, error) {
	at := r.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	tiers, err := marshalJSON(nonNilStrings(r.TiersAttempted))
	if err != nil {
		return 0, err
	}
	slots, err := marshalJSON(nonNilMap(r.Slots))
	if err != nil {
		return 0, err
	}
	latency, err := marshalJSON(nonNilInt64Map(r.LatencyMS))
	if err != nil {
		return 0, err
	}
	warnings, err := marshalJSON(nonNilStrings(r.Warnings))
	if err != nil {
		return 0, err
	}
	speculation, err := marshalJSON(nonNilAnyMap(r.Speculation))
	if err != nil {
		return 0, err
	}
	toolsUsed, err := marshalJSON(nonNilStrings(r.ToolsUsed))
	if err != nil {
		return 0, err
	}
	action, err := marshalJSON(nonNilMap(r.Action))
	if err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO route_log (
		turn_id, client_turn_id, at, channel, utterance, tiers_attempted,
		owner, answered_by, intent, intent_kind, intent_origin, slots,
		escalation_reason, latency_ms, total_ms, outcome, warnings, voice,
		partials, first_partial_lead_ms, speculation, speculation_model_calls,
		ack_ms, first_sentence_ms, tools_used, tools_attributed, quick_only,
		tool_signature, action, possible_miss, confirmed
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.TurnID, r.ClientTurnID, at.UnixNano(), r.Channel, r.Utterance, tiers,
		r.Owner, r.AnsweredBy, r.Intent, r.IntentKind, r.IntentOrigin, slots,
		r.EscalationReason, latency, r.TotalMS, r.Outcome, warnings, boolToInt(r.Voice),
		r.Partials, nullableInt64(r.FirstPartialLeadMS), speculation, r.SpeculationModelCalls,
		nullableInt64(r.AckMS), nullableInt64(r.FirstSentenceMS), toolsUsed, boolToInt(r.ToolsAttributed), boolToInt(r.QuickOnly),
		r.ToolSignature, action, boolToInt(r.PossibleMiss), boolToInt(r.Confirmed))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func nullableInt64(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func nonNilMap(v map[string]string) map[string]string {
	if v == nil {
		return map[string]string{}
	}
	return v
}

func nonNilInt64Map(v map[string]int64) map[string]int64 {
	if v == nil {
		return map[string]int64{}
	}
	return v
}

func nonNilAnyMap(v map[string]any) map[string]any {
	if v == nil {
		return map[string]any{}
	}
	return v
}

// MarkPossibleMiss flags a previously-written row as a possible reflex miss
// (the user rephrased or corrected shortly after a quick-tier answer).
func (s *Store) MarkPossibleMiss(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE route_log SET possible_miss = 1 WHERE id = ?`, id)
	return err
}

const routeRowColumns = `id, turn_id, client_turn_id, at, channel, utterance, tiers_attempted,
	owner, answered_by, intent, intent_kind, intent_origin, slots,
	escalation_reason, latency_ms, total_ms, outcome, warnings, voice,
	partials, first_partial_lead_ms, speculation, speculation_model_calls,
	ack_ms, first_sentence_ms, tools_used, tools_attributed, quick_only,
	tool_signature, action, possible_miss, confirmed`

func scanRouteRow(scan func(...any) error) (RouteRow, error) {
	var r RouteRow
	var atNS, totalMS int64
	var tiers, slots, latency, warnings, speculation, toolsUsed, action string
	var voice, toolsAttributed, quickOnly, possibleMiss, confirmed int64
	var firstPartialLeadMS, ackMS, firstSentenceMS sql.NullInt64
	if err := scan(
		&r.ID, &r.TurnID, &r.ClientTurnID, &atNS, &r.Channel, &r.Utterance, &tiers,
		&r.Owner, &r.AnsweredBy, &r.Intent, &r.IntentKind, &r.IntentOrigin, &slots,
		&r.EscalationReason, &latency, &totalMS, &r.Outcome, &warnings, &voice,
		&r.Partials, &firstPartialLeadMS, &speculation, &r.SpeculationModelCalls,
		&ackMS, &firstSentenceMS, &toolsUsed, &toolsAttributed, &quickOnly,
		&r.ToolSignature, &action, &possibleMiss, &confirmed,
	); err != nil {
		return RouteRow{}, err
	}
	r.At = time.Unix(0, atNS).UTC()
	r.TotalMS = totalMS
	r.Voice = voice != 0
	r.ToolsAttributed = toolsAttributed != 0
	r.QuickOnly = quickOnly != 0
	r.PossibleMiss = possibleMiss != 0
	r.Confirmed = confirmed != 0
	if firstPartialLeadMS.Valid {
		v := firstPartialLeadMS.Int64
		r.FirstPartialLeadMS = &v
	}
	if ackMS.Valid {
		v := ackMS.Int64
		r.AckMS = &v
	}
	if firstSentenceMS.Valid {
		v := firstSentenceMS.Int64
		r.FirstSentenceMS = &v
	}
	if err := json.Unmarshal([]byte(tiers), &r.TiersAttempted); err != nil {
		return RouteRow{}, err
	}
	if err := json.Unmarshal([]byte(slots), &r.Slots); err != nil {
		return RouteRow{}, err
	}
	if err := json.Unmarshal([]byte(latency), &r.LatencyMS); err != nil {
		return RouteRow{}, err
	}
	if err := json.Unmarshal([]byte(warnings), &r.Warnings); err != nil {
		return RouteRow{}, err
	}
	if err := json.Unmarshal([]byte(speculation), &r.Speculation); err != nil {
		return RouteRow{}, err
	}
	if err := json.Unmarshal([]byte(toolsUsed), &r.ToolsUsed); err != nil {
		return RouteRow{}, err
	}
	if err := json.Unmarshal([]byte(action), &r.Action); err != nil {
		return RouteRow{}, err
	}
	return r, nil
}

// ListRoutes returns rows at or after since, newest first.
func (s *Store) ListRoutes(ctx context.Context, since time.Time, limit int) ([]RouteRow, error) {
	q := `SELECT ` + routeRowColumns + ` FROM route_log WHERE at >= ? ORDER BY at DESC, id DESC`
	args := []any{since.UnixNano()}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RouteRow
	for rows.Next() {
		r, err := scanRouteRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// QuickOnlyRoutes returns rows at or after since whose main-path answer used
// only quick tools and was cleanly attributed (the promotion loop's
// candidate pool), newest first.
func (s *Store) QuickOnlyRoutes(ctx context.Context, since time.Time) ([]RouteRow, error) {
	q := `SELECT ` + routeRowColumns + ` FROM route_log WHERE at >= ? AND quick_only = 1 ORDER BY at DESC, id DESC`
	rows, err := s.db.QueryContext(ctx, q, since.UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RouteRow
	for rows.Next() {
		r, err := scanRouteRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// IntentAnswered returns the most recent rows a given intent answered
// (owner=quick), newest first, capped at limit. Used by the learned-intent
// breaker to compute a recent possible-miss rate.
func (s *Store) IntentAnswered(ctx context.Context, intent string, limit int) ([]RouteRow, error) {
	q := `SELECT ` + routeRowColumns + ` FROM route_log WHERE intent = ? AND owner = 'quick' ORDER BY at DESC, id DESC LIMIT ?`
	rows, err := s.db.QueryContext(ctx, q, intent, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RouteRow
	for rows.Next() {
		r, err := scanRouteRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RoutesByTurnIDs returns the route_log rows matching any of ids (order not
// guaranteed), for the promotion loop's draft step to recover a candidate's
// sample utterances (Design §16 item 2) from its Candidate.SampleTurnIDs.
// An empty ids returns (nil, nil) without a query.
func (s *Store) RoutesByTurnIDs(ctx context.Context, ids []string) ([]RouteRow, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	q := `SELECT ` + routeRowColumns + ` FROM route_log WHERE turn_id IN (` + strings.Join(placeholders, ",") + `)`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RouteRow
	for rows.Next() {
		r, err := scanRouteRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneRoutes deletes rows older than before and returns the count removed.
func (s *Store) PruneRoutes(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM route_log WHERE at < ?`, before.UnixNano())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
