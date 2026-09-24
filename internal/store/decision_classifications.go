package store

import (
	"context"
	"database/sql"
	"time"
)

// DecisionClassification is a cached decisions.Classifier verdict for one
// item, keyed by the item's own (Source, SourceID) identity.
type DecisionClassification struct {
	Source        string
	SourceID      string
	NeedsDecision bool
	TypeID        string
	Confidence    float64
	ClassifiedAt  time.Time
}

// GetDecisionClassification returns the cached classification for
// (source, sourceID). ok is false, with no error, when nothing is cached
// yet, exactly like GetCursor and GetBrief above.
func (s *Store) GetDecisionClassification(ctx context.Context, source, sourceID string) (c DecisionClassification, ok bool, err error) {
	var needsDecision int64
	var classifiedAt int64
	err = s.db.QueryRowContext(ctx,
		`SELECT needs_decision, type_id, confidence, classified_at FROM decision_classifications WHERE source = ? AND source_id = ?`,
		source, sourceID).Scan(&needsDecision, &c.TypeID, &c.Confidence, &classifiedAt)
	if err == sql.ErrNoRows {
		return DecisionClassification{}, false, nil
	}
	if err != nil {
		return DecisionClassification{}, false, err
	}
	c.Source, c.SourceID = source, sourceID
	c.NeedsDecision = needsDecision != 0
	c.ClassifiedAt = time.Unix(0, classifiedAt).UTC()
	return c, true, nil
}

// SetDecisionClassification upserts the cached classification for
// (c.Source, c.SourceID), overwriting whatever was there before.
func (s *Store) SetDecisionClassification(ctx context.Context, c DecisionClassification) error {
	needsDecision := int64(0)
	if c.NeedsDecision {
		needsDecision = 1
	}
	classifiedAt := c.ClassifiedAt
	if classifiedAt.IsZero() {
		classifiedAt = time.Now()
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO decision_classifications (source, source_id, needs_decision, type_id, confidence, classified_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (source, source_id) DO UPDATE SET
				needs_decision = excluded.needs_decision,
				type_id = excluded.type_id,
				confidence = excluded.confidence,
				classified_at = excluded.classified_at`,
		c.Source, c.SourceID, needsDecision, c.TypeID, c.Confidence, classifiedAt.UnixNano())
	return err
}

// DeleteDecisionClassification removes the cached classification for
// (source, sourceID), so the next lookup misses and the item is classified
// afresh. Deleting a row that isn't there is not an error.
func (s *Store) DeleteDecisionClassification(ctx context.Context, source, sourceID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM decision_classifications WHERE source = ? AND source_id = ?`, source, sourceID)
	return err
}
