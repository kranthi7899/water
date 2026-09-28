package promote

import (
	"fmt"
	"os"
	"path/filepath"
)

// LearnedDir is $WATER_HOME/twins/<twinID>/intents/learned: the promotion
// loop's overlay directory (Design §16 item 4). intents.LoadRegistry's
// LoadOptions.Learned points here, as an os.DirFS, only when
// router.promotion.enabled is on — with the flag off, the overlay is never
// loaded at all, regardless of what this directory contains.
func LearnedDir(home, twinID string) string {
	return filepath.Join(home, "twins", twinID, "intents", "learned")
}

// PendingDir is $WATER_HOME/twins/<twinID>/intents/pending: where the draft
// step (Design §16 item 2) writes a candidate's proposed intent file before
// it has been validated or promoted. Nothing ever loads files from here
// into a live registry — it exists only for the owner (via `water intent
// draft`, and the raw file itself) to review before ever deciding to
// promote one.
func PendingDir(home, twinID string) string {
	return filepath.Join(home, "twins", twinID, "intents", "pending")
}

// WriteLearned writes fileBytes to LearnedDir(home, twinID)/<id>.yaml,
// creating the directory if needed. id is the intent's own id (e.g.
// "learned.mail_from_priya"), never a candidate id. Directory and file
// permissions match this codebase's other owner-data writers
// (internal/store, internal/audit: 0o700 dirs, 0o600 files) — a twin's
// learned intents are executable matching logic, not mere configuration,
// so they get the same posture as the store and the audit log.
func WriteLearned(home, twinID string, fileBytes []byte, id string) (string, error) {
	return writeIntentFile(LearnedDir(home, twinID), id, fileBytes)
}

// WritePending writes fileBytes to
// PendingDir(home, twinID)/<candidateID>.yaml, creating the directory if
// needed, with the same permissions WriteLearned uses.
func WritePending(home, twinID string, fileBytes []byte, candidateID string) (string, error) {
	return writeIntentFile(PendingDir(home, twinID), candidateID, fileBytes)
}

func writeIntentFile(dir, name string, fileBytes []byte) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("promote: %w", err)
	}
	path := filepath.Join(dir, name+".yaml")
	if err := os.WriteFile(path, fileBytes, 0o600); err != nil {
		return "", fmt.Errorf("promote: %w", err)
	}
	return path, nil
}
