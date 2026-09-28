package memory

// Sensitivity is a record's sensitivity tier.
//
// PROVISIONAL (owner decision at F's Approve, 2026-09-25): the tier names
// below are a placeholder set. The owner names the real tiers as part of
// the global "which outputs always require CEO review" question, and this
// file is the one place they change. F gives the levels no behavior of
// their own; filtering by sensitivity is only a Query parameter.
type Sensitivity string

const (
	Normal     Sensitivity = "normal"
	Sensitive  Sensitivity = "sensitive"
	Restricted Sensitivity = "restricted"
)

// Sensitivities is the closed set, in order from least to most sensitive.
var Sensitivities = []Sensitivity{Normal, Sensitive, Restricted}

// Valid reports whether s is in the closed set.
func (s Sensitivity) Valid() bool {
	for _, v := range Sensitivities {
		if s == v {
			return true
		}
	}
	return false
}
