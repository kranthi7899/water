package nervous

import "testing"

func TestToolTraceSingleInFlightIsAttributed(t *testing.T) {
	tt := NewToolTracer()
	tt.BeginMain("turn-1")
	tt.RecordUse("quick__calendar")
	tt.RecordUse("gcal__list_events")
	used, attributed := tt.EndMain("turn-1")

	if !attributed {
		t.Fatal("a single in-flight turn must stay attributed")
	}
	if len(used) != 2 || used[0] != "quick__calendar" || used[1] != "gcal__list_events" {
		t.Fatalf("used = %v, want [quick__calendar gcal__list_events]", used)
	}
}

func TestToolTraceTwoInFlightBothUnattributed(t *testing.T) {
	tt := NewToolTracer()
	tt.BeginMain("turn-1")
	tt.BeginMain("turn-2")
	tt.RecordUse("quick__calendar")

	used1, attr1 := tt.EndMain("turn-1")
	used2, attr2 := tt.EndMain("turn-2")

	if attr1 || attr2 {
		t.Fatalf("both turns must be unattributed while 2 were in flight: attr1=%v attr2=%v", attr1, attr2)
	}
	if len(used1) != 1 || len(used2) != 1 {
		t.Fatalf("the call should be appended to both turns: used1=%v used2=%v", used1, used2)
	}
}

func TestToolTraceZeroInFlightIsDropped(t *testing.T) {
	tt := NewToolTracer()
	tt.RecordUse("quick__calendar") // no BeginMain at all: must not panic or fabricate a turn
	// Nothing to assert beyond "did not panic" — there is no turn id to
	// check EndMain against, which is exactly the point: the call is
	// dropped, not attributed to a phantom turn.
}

func TestToolTraceStaysAmbiguousOnceMarked(t *testing.T) {
	tt := NewToolTracer()
	tt.BeginMain("turn-1")
	tt.BeginMain("turn-2")
	tt.RecordUse("quick__calendar") // ambiguous: both marked unattributed
	_, _ = tt.EndMain("turn-2")     // turn-2 leaves; only turn-1 remains in flight

	tt.RecordUse("quick__latest_mail") // now only turn-1 is in flight...
	used, attributed := tt.EndMain("turn-1")

	if attributed {
		t.Fatal("turn-1 was already marked unattributed by the earlier ambiguous call and must stay that way")
	}
	if len(used) != 2 {
		t.Fatalf("used = %v, want both calls recorded", used)
	}
}

func TestQuickOnlySignature(t *testing.T) {
	quickOnly, sig := QuickOnlySignature([]string{"quick__next_event", "quick__calendar", "quick__calendar"}, "quick__")
	if !quickOnly {
		t.Fatal("expected quickOnly=true")
	}
	if sig != "quick__calendar+quick__next_event" {
		t.Fatalf("signature = %q, want deduplicated and sorted", sig)
	}

	quickOnly, sig = QuickOnlySignature([]string{"quick__calendar", "gcal__list_events"}, "quick__")
	if quickOnly || sig != "" {
		t.Fatalf("a non-quick tool must make quickOnly false and the signature empty, got (%v, %q)", quickOnly, sig)
	}

	quickOnly, sig = QuickOnlySignature(nil, "quick__")
	if quickOnly || sig != "" {
		t.Fatalf("empty input must report (false, \"\"), got (%v, %q)", quickOnly, sig)
	}
}
