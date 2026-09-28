package dashboards

import (
	"testing"
	"testing/fstest"

	"water"
)

const validFinanceYAML = `
name: Finance
source: company_finance
metrics: [cash_position, monthly_burn, runway_months]
breakdown: spend_by_application
callout: worst_app_margin
`

func TestParseSpecValid(t *testing.T) {
	s, err := ParseSpec([]byte(validFinanceYAML), "finance")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "finance" || s.Source != "company_finance" || len(s.Metrics) != 3 {
		t.Fatalf("spec = %+v", s)
	}
}

func TestParseSpecRejectsUnknownMetricID(t *testing.T) {
	bad := `
name: Finance
source: company_finance
metrics: [cash_position, monthly_burn, made_up_metric]
breakdown: spend_by_application
callout: worst_app_margin
`
	_, err := ParseSpec([]byte(bad), "finance")
	if err == nil {
		t.Fatal("expected an error for an unknown metric id, got nil")
	}
}

func TestParseSpecRejectsUnknownBreakdownID(t *testing.T) {
	bad := `
name: Finance
source: company_finance
metrics: [cash_position, monthly_burn, runway_months]
breakdown: made_up_breakdown
callout: worst_app_margin
`
	if _, err := ParseSpec([]byte(bad), "finance"); err == nil {
		t.Fatal("expected an error for an unknown breakdown id, got nil")
	}
}

func TestParseSpecRejectsUnknownCalloutID(t *testing.T) {
	bad := `
name: Finance
source: company_finance
metrics: [cash_position, monthly_burn, runway_months]
breakdown: spend_by_application
callout: made_up_callout
`
	if _, err := ParseSpec([]byte(bad), "finance"); err == nil {
		t.Fatal("expected an error for an unknown callout id, got nil")
	}
}

// TestParseSpecRejectsNumericLiteral is the brief's own acceptance test: a
// numeric-literal-shaped value (never an expression or a hardcoded number)
// must be rejected by the id-shape check, distinct from "unknown id".
func TestParseSpecRejectsNumericLiteral(t *testing.T) {
	bad := `
name: Finance
source: company_finance
metrics: [cash_position, monthly_burn, "12.5"]
breakdown: spend_by_application
callout: worst_app_margin
`
	_, err := ParseSpec([]byte(bad), "finance")
	if err == nil {
		t.Fatal("expected an error for a numeric-literal-shaped metric, got nil")
	}
}

func TestParseSpecRejectsExpressionShapedValue(t *testing.T) {
	bad := `
name: Finance
source: company_finance
metrics: [cash_position, monthly_burn, runway_months]
breakdown: "revenue-cost"
callout: worst_app_margin
`
	if _, err := ParseSpec([]byte(bad), "finance"); err == nil {
		t.Fatal("expected an error for an expression-shaped breakdown, got nil")
	}
}

func TestParseSpecRejectsWrongMetricCount(t *testing.T) {
	bad := `
name: Finance
source: company_finance
metrics: [cash_position, monthly_burn]
breakdown: spend_by_application
callout: worst_app_margin
`
	if _, err := ParseSpec([]byte(bad), "finance"); err == nil {
		t.Fatal("expected an error for only 2 metrics, got nil")
	}
}

func TestParseSpecRejectsBadSource(t *testing.T) {
	bad := `
name: Finance
source: not_a_real_source
metrics: [cash_position, monthly_burn, runway_months]
breakdown: spend_by_application
callout: worst_app_margin
`
	if _, err := ParseSpec([]byte(bad), "finance"); err == nil {
		t.Fatal("expected an error for a bad source, got nil")
	}
}

func TestParseSpecRejectsUnknownField(t *testing.T) {
	bad := validFinanceYAML + "\nbogus_field: 1\n"
	if _, err := ParseSpec([]byte(bad), "finance"); err == nil {
		t.Fatal("expected an error for an unknown field, got nil")
	}
}

func TestLoadRegistryMissingDirIsEmpty(t *testing.T) {
	r, err := LoadRegistry(fstest.MapFS{}, "nobody")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Specs()) != 0 {
		t.Fatalf("Specs = %+v, want none", r.Specs())
	}
}

func TestLoadRegistryRejectsBadFile(t *testing.T) {
	fsys := fstest.MapFS{
		"twins/t/dashboards/finance.yaml": &fstest.MapFile{Data: []byte("name: [not, a, string]\n")},
	}
	if _, err := LoadRegistry(fsys, "t"); err == nil {
		t.Fatal("expected an error for a malformed dashboard file, got nil")
	}
}

func TestLoadRegistryLoadsAllThreeRealFiles(t *testing.T) {
	r, err := LoadRegistry(water.TwinsFS(), "ceo")
	if err != nil {
		t.Fatal(err)
	}
	specs := r.Specs()
	if len(specs) != 3 {
		t.Fatalf("got %d specs, want 3: %+v", len(specs), specs)
	}
	for _, id := range []string{"finance", "delivery", "clients"} {
		s, ok := r.Spec(id)
		if !ok {
			t.Errorf("missing expected dashboard %q", id)
			continue
		}
		if len(s.Metrics) != 3 || s.Breakdown == "" || s.Callout == "" {
			t.Errorf("dashboard %q incomplete: %+v", id, s)
		}
	}
}
