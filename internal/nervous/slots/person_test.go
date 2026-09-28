package slots

import "testing"

func testEntities() Entities {
	return Entities{People: []Person{
		{Name: "Alex Chen", Email: "alex.chen@example.com"},
		{Name: "Alex Rivera", Email: "alex.rivera@example.com"},
		{Name: "Jordan Lee", Email: "jordan@example.com"},
	}}
}

func TestResolvePersonExactEmail(t *testing.T) {
	v, outcome, _ := Resolve(TypePerson, capN("alex.chen@example.com"), Spec{}, ref, testEntities())
	if outcome != Resolved {
		t.Fatalf("outcome = %v, want Resolved", outcome)
	}
	if v.Person.Email != "alex.chen@example.com" {
		t.Errorf("Person.Email = %q, want alex.chen@example.com", v.Person.Email)
	}
}

func TestResolvePersonExactFullName(t *testing.T) {
	v, outcome, _ := Resolve(TypePerson, capN("jordan", "lee"), Spec{}, ref, testEntities())
	if outcome != Resolved {
		t.Fatalf("outcome = %v, want Resolved", outcome)
	}
	if v.Person.Email != "jordan@example.com" {
		t.Errorf("Person.Email = %q, want jordan@example.com", v.Person.Email)
	}
}

func TestResolvePersonUniqueFirstName(t *testing.T) {
	v, outcome, _ := Resolve(TypePerson, capN("jordan"), Spec{}, ref, testEntities())
	if outcome != Resolved {
		t.Fatalf("outcome = %v, want Resolved", outcome)
	}
	if v.Person.Email != "jordan@example.com" {
		t.Errorf("Person.Email = %q, want jordan@example.com", v.Person.Email)
	}
}

func TestResolvePersonUniqueLastName(t *testing.T) {
	v, outcome, _ := Resolve(TypePerson, capN("chen"), Spec{}, ref, testEntities())
	if outcome != Resolved {
		t.Fatalf("outcome = %v, want Resolved", outcome)
	}
	if v.Person.Email != "alex.chen@example.com" {
		t.Errorf("Person.Email = %q, want alex.chen@example.com", v.Person.Email)
	}
}

func TestResolvePersonAmbiguous(t *testing.T) {
	_, outcome, opts := Resolve(TypePerson, capN("alex"), Spec{}, ref, testEntities())
	if outcome != Ambiguous {
		t.Fatalf("outcome = %v, want Ambiguous", outcome)
	}
	if len(opts) != 2 {
		t.Fatalf("options = %v, want 2 entries", opts)
	}
	found := map[string]bool{}
	for _, o := range opts {
		found[o] = true
	}
	if !found["Alex Chen <alex.chen@example.com>"] || !found["Alex Rivera <alex.rivera@example.com>"] {
		t.Errorf("options = %v, missing an expected Alex", opts)
	}
}

func TestResolvePersonUnknown(t *testing.T) {
	_, outcome, _ := Resolve(TypePerson, capN("taylor"), Spec{}, ref, testEntities())
	if outcome != Unresolved {
		t.Errorf("outcome = %v, want Unresolved", outcome)
	}
}

func TestResolvePersonEmptyCapture(t *testing.T) {
	_, outcome, _ := Resolve(TypePerson, capN(), Spec{}, ref, testEntities())
	if outcome != Unresolved {
		t.Errorf("outcome = %v, want Unresolved", outcome)
	}
}
