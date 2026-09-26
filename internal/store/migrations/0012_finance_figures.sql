-- finance_figures holds one normalized record per company_finance lookup
-- (internal/connectors/google/gsheets): a specific tab and A1 range read
-- live from the company's real financial model Google Sheet. source_id is
-- always "<Tab>!<Range>" (e.g. "Cash & runway!B19:B21"), so it doubles as
-- the exact citation a decision card's evidence needs — no separate tab/
-- range columns are required for that, but they're kept as their own
-- columns too since a query filtering by tab alone (independent of the
-- exact range string) is a real, likely-future need this shouldn't have to
-- be re-migrated for.
CREATE TABLE finance_figures (
	id INTEGER PRIMARY KEY,
	source TEXT NOT NULL,
	source_id TEXT NOT NULL,
	external INTEGER NOT NULL,
	created_at INTEGER,
	updated_at INTEGER,
	tab TEXT NOT NULL DEFAULT '',
	range_a1 TEXT NOT NULL DEFAULT '',
	-- values_json is a JSON object whose shape varies per function (e.g.
	-- cash_position: {"cash_usd":...,"burn_usd":...,"runway_months":...};
	-- budget_status: {"application":...,"budget_usd":...,"actual_usd":...,
	-- "variance_usd":...,"revenue_usd":...}) — deliberately opaque here,
	-- the same design already used for Person.Identities
	-- (roster_records.go): one record type, many call-site-defined shapes,
	-- rather than a table per function.
	values_json TEXT NOT NULL DEFAULT '{}',
	UNIQUE (source, source_id)
);
