-- List pages every record table by created_at (filter and ORDER BY
-- created_at DESC, id DESC), and EventsInRange filters and sorts events by
-- start_at. Without these, the brief, the decision trigger and the fast
-- path scan and sort whole tables whose size grows with the mailbox.
CREATE INDEX IF NOT EXISTS messages_created_at ON messages (created_at);
CREATE INDEX IF NOT EXISTS meetings_created_at ON meetings (created_at);
CREATE INDEX IF NOT EXISTS events_created_at ON events (created_at);
CREATE INDEX IF NOT EXISTS documents_created_at ON documents (created_at);
CREATE INDEX IF NOT EXISTS issues_created_at ON issues (created_at);
CREATE INDEX IF NOT EXISTS commits_created_at ON commits (created_at);
CREATE INDEX IF NOT EXISTS transactions_created_at ON transactions (created_at);
CREATE INDEX IF NOT EXISTS contacts_created_at ON contacts (created_at);
CREATE INDEX IF NOT EXISTS events_start_at ON events (start_at);
