-- docs/slices/UI.md Phase 3d: meeting_sessions gains the recap's structured
-- signal block plus its project guess, written once at recap-on-stop
-- (internal/meetings.Manager.Recap, via Store.SetMeetingRecapSignals).
--
-- recap_signals is JSON of meetings.RecapSignals (decisions, action items,
-- open questions, FYI -- the same code-extracted signals the recap's one
-- phrasing model call is handed, never model output itself).
--
-- project_guess_id/project_guess_confidence are the recap's project guess:
-- a classifier's chosen project id (internal/store's "projects" table,
-- source "seed") and its confidence, always shown as a labelled guess and
-- never written as a for_project link or any other anchor on their own.
--
-- All three are nullable and left NULL by InsertMeetingSession: a session
-- whose recap hasn't run yet, was skipped or failed, or whose project guess
-- came back unavailable, simply has no value here.
ALTER TABLE meeting_sessions ADD COLUMN recap_signals TEXT;
ALTER TABLE meeting_sessions ADD COLUMN project_guess_id TEXT;
ALTER TABLE meeting_sessions ADD COLUMN project_guess_confidence REAL;
