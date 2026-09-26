package connectors_test

import (
	"strings"
	"testing"

	"water/internal/connectors"
	"water/internal/connectors/github"
	"water/internal/connectors/google/gcal"
	"water/internal/connectors/google/gdrive"
	"water/internal/connectors/google/gmail"
	"water/internal/connectors/google/gsheets"
	"water/internal/connectors/hubspot"
	"water/internal/connectors/linear"
	"water/internal/twins"
)

// TestFunctionLabelFallback: a step label is Activity, else Description,
// else the caller's fallback (the function id).
func TestFunctionLabelFallback(t *testing.T) {
	cases := []struct {
		f    connectors.Function
		want string
	}{
		{connectors.Function{Activity: "Checking your calendar", Description: "List events."}, "Checking your calendar"},
		{connectors.Function{Activity: "  ", Description: "List events."}, "List events."},
		{connectors.Function{}, "gcal.list_events"},
	}
	for _, c := range cases {
		if got := c.f.Label("gcal.list_events"); got != c.want {
			t.Errorf("Label(%+v) = %q, want %q", c.f, got, c.want)
		}
	}
}

// TestRealReadFunctionsHaveActivity: every read (level R) function of the
// CEO's real connectors carries a short, fixed, CEO-facing Activity line,
// so the HUD never falls back to a model-facing description for them.
func TestRealReadFunctionsHaveActivity(t *testing.T) {
	conns := []connectors.Connector{&gcal.Calendar{}, &gmail.Gmail{}, &gdrive.Drive{}, &gsheets.Sheets{},
		&github.GitHub{}, &linear.Linear{}, &hubspot.HubSpot{}}
	for _, c := range conns {
		for _, f := range c.Functions() {
			if f.Level != twins.R {
				continue
			}
			id := c.Name() + "." + f.Name
			a := strings.TrimSpace(f.Activity)
			if a == "" {
				t.Errorf("%s: no Activity", id)
				continue
			}
			if len(a) > 48 || strings.ContainsAny(a, "{}%\n") || strings.HasSuffix(a, ".") {
				t.Errorf("%s: Activity %q should be a short plain phrase", id, a)
			}
		}
	}
}
