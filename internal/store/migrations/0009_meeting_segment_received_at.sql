-- received_at is when the daemon stored a segment, as opposed to at, the
-- client's "when the speech began". Help's and cues' rolling windows select
-- on received_at: a long utterance is posted ~45s after it began, which put
-- it outside the 45s cue window before it was ever stored. at still orders
-- the transcript. Numbered 0009 because 0008 is taken on another branch.
ALTER TABLE meeting_segments ADD COLUMN received_at INTEGER;
UPDATE meeting_segments SET received_at = at WHERE received_at IS NULL;
CREATE INDEX meeting_segments_session_received ON meeting_segments (session_id, received_at);
