-- docs/slices/UI.md Phase 0 (0a): bulk-mail signals gmail.go now captures,
-- so internal/mailnoise can classify a message as noise/informative from
-- general header/label/sender shape instead of a per-domain denylist.
--
-- labels is a JSON array (store.Message's []string convention, see
-- internal/store/records.go's encode/scanTarget); list_unsubscribe, list_id,
-- precedence and auto_submitted are the raw header values, or '' when
-- absent. Old rows stay at these defaults until re-fetched, same as 0006's
-- body_full precedent.
ALTER TABLE messages ADD COLUMN labels TEXT NOT NULL DEFAULT '[]';
ALTER TABLE messages ADD COLUMN list_unsubscribe TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN list_id TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN precedence TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN auto_submitted TEXT NOT NULL DEFAULT '';
