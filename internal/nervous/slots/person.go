package slots

import (
	"strings"

	"water/internal/nervous/tmpl"
)

func personValue(p Person) Value {
	label := strings.ToLower(p.Name) + " <" + strings.ToLower(p.Email) + ">"
	return Value{Type: TypePerson, Label: label, Spoken: p.Name, Person: p}
}

func dedupePeople(in []Person) []Person {
	seen := make(map[string]bool, len(in))
	out := make([]Person, 0, len(in))
	for _, p := range in {
		key := strings.ToLower(p.Email)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	return out
}

// resolvePerson resolves a captured name against ents.People: an exact
// email or full-name match resolves outright; a first or last name unique
// among the entities resolves; more than one candidate is Ambiguous with
// every match listed; no candidate is Unresolved.
func resolvePerson(c tmpl.Capture, ents Entities) (Value, Outcome, []string) {
	raw := strings.ToLower(strings.TrimSpace(strings.Join(c.Tokens, " ")))
	if raw == "" {
		return Value{Type: TypePerson}, Unresolved, nil
	}

	for _, p := range ents.People {
		if strings.ToLower(p.Email) == raw {
			return personValue(p), Resolved, nil
		}
	}
	for _, p := range ents.People {
		if strings.ToLower(p.Name) == raw {
			return personValue(p), Resolved, nil
		}
	}

	var matches []Person
	for _, p := range ents.People {
		for _, part := range strings.Fields(strings.ToLower(p.Name)) {
			if part == raw {
				matches = append(matches, p)
				break
			}
		}
	}
	matches = dedupePeople(matches)

	switch len(matches) {
	case 0:
		return Value{Type: TypePerson}, Unresolved, nil
	case 1:
		return personValue(matches[0]), Resolved, nil
	default:
		opts := make([]string, 0, len(matches))
		for _, m := range matches {
			opts = append(opts, m.Name+" <"+m.Email+">")
		}
		return Value{Type: TypePerson}, Ambiguous, opts
	}
}
