package reflex

import "testing"

func TestQuickToolsAreEligibleAndUnique(t *testing.T) {
	seen := map[string]string{}
	for id, h := range Table() {
		if h.Spec.QuickTool == "" {
			continue
		}
		if !h.Spec.QuickEligible() {
			t.Errorf("%s declares QuickTool=%q but is not QuickEligible (ReadOnly=%v SideEffects=%v Class=%v)",
				id, h.Spec.QuickTool, h.Spec.ReadOnly, h.Spec.SideEffects, h.Spec.Class)
		}
		if other, dup := seen[h.Spec.QuickTool]; dup {
			t.Errorf("QuickTool %q declared by both %s and %s", h.Spec.QuickTool, other, id)
		}
		seen[h.Spec.QuickTool] = id
	}
}

func TestTableIDsMatchSpecIDs(t *testing.T) {
	for id, h := range Table() {
		if h.Spec.ID != id {
			t.Errorf("Table key %q has Spec.ID %q", id, h.Spec.ID)
		}
	}
}

func TestSpecsProjectsTable(t *testing.T) {
	specs := Specs()
	table := Table()
	if len(specs) != len(table) {
		t.Fatalf("Specs() has %d entries, Table() has %d", len(specs), len(table))
	}
	for id, h := range table {
		if specs[id].ID != h.Spec.ID {
			t.Errorf("Specs()[%s] = %+v, want %+v", id, specs[id], h.Spec)
		}
	}
}
