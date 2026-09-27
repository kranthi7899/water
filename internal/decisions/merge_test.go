package decisions

import "testing"

func TestMergeRecordWinsOverComputedCard(t *testing.T) {
	computed := []*Card{{ID: "card-1", Lead: "computed lead", Recommendation: "computed rec"}}
	records := []*Card{{ID: "card-1", Lead: "record lead", Recommendation: "record rec", TeamSignal: []Signal{{Person: "Nina", Status: "responded", Simulated: true}}}}

	out := Merge(computed, records, nil)
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1", len(out))
	}
	if out[0].Lead != "record lead" || out[0].Recommendation != "record rec" {
		t.Fatalf("got %+v, want the record's own fields to show through, not the computed card's", out[0])
	}
	if len(out[0].TeamSignal) != 1 || out[0].TeamSignal[0].Person != "Nina" {
		t.Fatalf("got %+v, want the record's TeamSignal (computed cards never set one)", out[0].TeamSignal)
	}
}

func TestMergeExtraEvidenceAppendedAndTainted(t *testing.T) {
	computed := []*Card{{ID: "card-1", Lead: "lead", Evidence: []Evidence{{Text: "original", Source: "code:x"}}, Untrusted: false}}
	extra := []EvidenceExtra{{CardID: "card-1", Source: "research:run-1", Text: "attached report", Untrusted: true}}

	out := Merge(computed, nil, extra)
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1", len(out))
	}
	c := out[0]
	if len(c.Evidence) != 2 {
		t.Fatalf("Evidence = %+v, want the original plus the appended extra", c.Evidence)
	}
	if c.Evidence[0].Text != "original" {
		t.Fatalf("original evidence should stay first: %+v", c.Evidence)
	}
	appended := c.Evidence[1]
	if appended.Text != "attached report" || appended.Source != "research:run-1" || !appended.Untrusted {
		t.Fatalf("appended evidence = %+v, want the extra row's own fields, tainted", appended)
	}
	if !c.Untrusted {
		t.Fatal("a card that was otherwise trusted must read as untrusted once tainted extra evidence is merged in; Merge must not drop that silently")
	}

	// The input slice itself must be untouched: Merge must not mutate the
	// caller's card in place.
	if computed[0].Untrusted {
		t.Fatal("Merge mutated the caller's input card")
	}
	if len(computed[0].Evidence) != 1 {
		t.Fatal("Merge mutated the caller's input card's evidence slice")
	}
}

func TestMergeUntaintedExtraEvidenceLeavesCardTrusted(t *testing.T) {
	computed := []*Card{{ID: "card-1", Evidence: []Evidence{{Text: "x", Source: "code:x"}}}}
	extra := []EvidenceExtra{{CardID: "card-1", Source: "s", Text: "clean addition", Untrusted: false}}

	out := Merge(computed, nil, extra)
	if out[0].Untrusted {
		t.Fatal("an untainted extra evidence row must not flip a trusted card to untrusted")
	}
}

func TestMergePassesThroughACardWithNoRecordAndNoExtra(t *testing.T) {
	computed := []*Card{{ID: "card-1", Lead: "unchanged"}}
	out := Merge(computed, nil, nil)
	if len(out) != 1 || out[0].Lead != "unchanged" {
		t.Fatalf("got %+v", out)
	}
	if out[0] != computed[0] {
		t.Fatal("a card with no record and no extra evidence should pass through as the same value")
	}
}

func TestMergeIncludesARecordOnlyID(t *testing.T) {
	computed := []*Card{{ID: "card-computed", Lead: "computed"}}
	records := []*Card{{ID: "card-seed", Lead: "seeded, no live computed card"}}

	out := Merge(computed, records, nil)
	if len(out) != 2 {
		t.Fatalf("len(out) = %d, want 2 (one computed, one record-only)", len(out))
	}
	var sawSeed bool
	for _, c := range out {
		if c.ID == "card-seed" {
			sawSeed = true
			if c.Lead != "seeded, no live computed card" {
				t.Fatalf("record-only card = %+v", c)
			}
		}
	}
	if !sawSeed {
		t.Fatal("a card id present only in records must still appear in the merged result")
	}
}

func TestMergeExtraEvidenceForARecordOnlyID(t *testing.T) {
	records := []*Card{{ID: "card-seed", Evidence: []Evidence{{Text: "seed evidence", Source: "code:seed"}}}}
	extra := []EvidenceExtra{{CardID: "card-seed", Source: "s", Text: "attached later", Untrusted: true}}

	out := Merge(nil, records, extra)
	if len(out) != 1 {
		t.Fatalf("len(out) = %d, want 1", len(out))
	}
	if len(out[0].Evidence) != 2 || !out[0].Untrusted {
		t.Fatalf("got %+v, want extra evidence appended and tainted even for a record-only card", out[0])
	}
}
