-- body_full marks messages.body as the full text rather than a preview
-- (a Gmail list snippet), so Upsert can refuse to downgrade a full body
-- that get_message already stored when a later list re-ingests the same
-- message.
ALTER TABLE messages ADD COLUMN body_full INTEGER NOT NULL DEFAULT 0;
