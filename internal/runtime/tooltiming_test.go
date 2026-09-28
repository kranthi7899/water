package runtime

import (
	"testing"
	"time"
)

// TestToolSpansWithin: spans are returned only when wholly inside the
// window, invalid spans are dropped, and the ring keeps the newest
// toolSpanCap.
func TestToolSpansWithin(t *testing.T) {
	base := time.Unix(1_900_000_000, 0)
	at := func(s int) time.Time { return base.Add(time.Duration(s) * time.Second) }
	NoteToolSpan(ToolSpan{Function: "research.web", Start: at(10), End: at(15), Phases: map[string]int64{"research_total": 5000}})
	NoteToolSpan(ToolSpan{Function: "gmail.search", Start: at(5), End: at(12)}) // starts before the window
	NoteToolSpan(ToolSpan{Function: "x", Start: at(20), End: at(19)})           // ends before it starts: dropped
	NoteToolSpan(ToolSpan{Start: at(11), End: at(12)})                          // no function: dropped
	got := ToolSpansWithin(at(8), at(30))
	if len(got) != 1 || got[0].Function != "research.web" || got[0].Phases["research_total"] != 5000 {
		t.Fatalf("spans = %+v", got)
	}
	for i := 0; i < toolSpanCap+10; i++ {
		NoteToolSpan(ToolSpan{Function: "f", Start: at(100 + i), End: at(100 + i)})
	}
	if n := len(ToolSpansWithin(at(0), at(1000))); n != toolSpanCap {
		t.Fatalf("ring holds %d, want %d", n, toolSpanCap)
	}
	if len(ToolSpansWithin(at(8), at(30))) != 0 {
		t.Fatal("oldest span not evicted")
	}
}
