// Package slots resolves a captured, typed argument (a tmpl.Capture, or a
// raw string for quick-tool callers) into a typed Value: a date,
// time, count, person, and so on. Resolution never guesses: an expression
// outside the recognized vocabulary is Unresolved, and a person name that
// matches more than one entity is Ambiguous with the candidates listed, so
// callers can escalate rather than pick silently.
package slots

import (
	"time"

	"water/internal/nervous/tmpl"
)

// Type is the kind of value a slot resolves to.
type Type string

const (
	TypeDate      Type = "date"
	TypeDateRange Type = "daterange"
	TypeTime      Type = "time"
	TypePartOfDay Type = "part_of_day"
	TypeDuration  Type = "duration"
	TypeCount     Type = "count"
	TypePerson    Type = "person"
	TypeText      Type = "text"
	TypeProject   Type = "project" // reserved; Resolve always returns Unresolved
)

// Outcome is the result of a resolution attempt.
type Outcome int

const (
	Resolved Outcome = iota
	Unresolved
	Ambiguous
)

// Person is one resolvable entity. A later task builds the Entities list
// this resolves against from distinct message senders.
type Person struct {
	Name  string
	Email string
}

// Entities is the set of known people (and, later, projects) a slot
// resolves person/project captures against.
type Entities struct {
	People []Person
}

// Spec carries slot-declaration bounds. Only count consults Min/Max today.
type Spec struct {
	Min *int
	Max *int
}

// Value is a single resolved slot value. Only the fields relevant to Type
// are meaningful; the rest are left zero.
type Value struct {
	Type   Type
	Label  string // canonical, eval-comparable form: "tomorrow", "3pm"
	Spoken string // human interpretation echoed in an answer header

	Start, End time.Time     // date/daterange/part_of_day: half-open range
	At         time.Time     // time: hour/minute set on a zero-value date
	Dur        time.Duration // duration
	N          int           // count
	Person     Person        // person
	Text       string        // text
}

// Resolve resolves one captured slot span into a typed Value. now anchors
// every relative date/time expression, and ents supplies the known people
// (and, later, projects) a person/project capture resolves against.
func Resolve(t Type, c tmpl.Capture, spec Spec, now time.Time, ents Entities) (Value, Outcome, []string) {
	switch t {
	case TypeDate:
		return resolveDate(c, now)
	case TypeDateRange:
		return resolveDateRange(c, now)
	case TypeTime:
		return resolveTime(c)
	case TypePartOfDay:
		return resolvePartOfDay(c, now)
	case TypeDuration:
		return resolveDuration(c)
	case TypeCount:
		return resolveCount(c, spec)
	case TypePerson:
		return resolvePerson(c, ents)
	case TypeText:
		return resolveText(c)
	case TypeProject:
		// Reserved for a later slice: no project source exists yet.
		return Value{Type: TypeProject}, Unresolved, nil
	default:
		return Value{Type: t}, Unresolved, nil
	}
}

// ResolveString resolves a plain string the way Resolve resolves a template
// capture, for callers with no tmpl.Capture: an intent's own declared
// default value, and quick-tool string arguments. A text slot must keep its
// original casing and punctuation, so s is used verbatim as the capture's
// Raw, while Tokens come from the same normalization every other type
// matches against.
func ResolveString(t Type, s string, spec Spec, now time.Time, ents Entities) (Value, Outcome, []string) {
	u := tmpl.Normalize(s, nil)
	c := tmpl.Capture{Tokens: u.Tokens, Raw: s}
	return Resolve(t, c, spec, now, ents)
}
