package gdrive

import "testing"

func TestDriveQuery(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "trashed = false"},
		{"budget report", "trashed = false and (fullText contains 'budget report' or name contains 'budget report')"},
		{`O'Brien's plan`, `trashed = false and (fullText contains 'O\'Brien\'s plan' or name contains 'O\'Brien\'s plan')`},
		{`back\slash`, `trashed = false and (fullText contains 'back\\slash' or name contains 'back\\slash')`},
		// A plain-language query that happens to contain a field+operator
		// pattern is treated as a deliberate raw query, per spec, and passed
		// through unescaped inside its own clause rather than double-quoted.
		{`x' or name contains '`, `trashed = false and (x' or name contains ')`},
		{"mimeType = 'application/vnd.google-apps.document'", "trashed = false and (mimeType = 'application/vnd.google-apps.document')"},
		{"'1abc' in parents", "trashed = false and ('1abc' in parents)"},
		{"  spaced query  ", "trashed = false and (fullText contains 'spaced query' or name contains 'spaced query')"},
	}
	for _, c := range cases {
		if got := driveQuery(c.in); got != c.want {
			t.Errorf("driveQuery(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestEscapeQuoted(t *testing.T) {
	if got := escapeQuoted(`it's a "test" \ path`); got != `it\'s a "test" \\ path` {
		t.Fatalf("got %q", got)
	}
}
