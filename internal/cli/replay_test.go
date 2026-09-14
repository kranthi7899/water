package cli

import (
	"testing"

	"water/internal/trace"
)

func TestPickRecord(t *testing.T) {
	recs := []trace.CallRecord{{Seq: 1, Role: "ceo"}, {Seq: 2, Role: "cto"}, {Seq: 3, Role: "coo"}, {Seq: 4, Role: "cto"}}
	if r, err := pickRecord(recs, "2"); err != nil || r.Seq != 2 {
		t.Fatalf("by seq: %+v %v", r, err)
	}
	if r, err := pickRecord(recs, "CTO"); err != nil || r.Seq != 4 {
		t.Fatalf("by role picks the last call: %+v %v", r, err)
	}
	if _, err := pickRecord(recs, "design"); err == nil {
		t.Fatal("missing role should error")
	}
	if _, err := pickRecord(recs, "9"); err == nil {
		t.Fatal("missing seq should error")
	}
}
