package approvals

import (
	"bytes"
	"encoding/json"
)

// Answer is the interpreted reply to an approval prompt.
type Answer int

const (
	Ambiguous Answer = iota // the zero value: anything unclear is not a yes
	No
	Yes
)

func (a Answer) String() string {
	switch a {
	case Yes:
		return "yes"
	case No:
		return "no"
	}
	return "ambiguous"
}

func decodeJSON(s string, v any) error {
	dec := json.NewDecoder(bytes.NewReader([]byte(s)))
	dec.UseNumber()
	return dec.Decode(v)
}
