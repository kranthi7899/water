package gapi

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// ArgString returns args[k] trimmed, or "".
func ArgString(args map[string]any, k string) string {
	s, _ := args[k].(string)
	return strings.TrimSpace(s)
}

// ArgInt reads an integer argument (json.Number, float64 or int), returning
// def when absent and clamping the value to [lo, hi].
func ArgInt(args map[string]any, k string, def, lo, hi int) (int, error) {
	v, ok := args[k]
	if !ok || v == nil {
		return def, nil
	}
	var n int64
	switch x := v.(type) {
	case json.Number:
		i, err := x.Int64()
		if err != nil {
			f, ferr := x.Float64()
			if ferr != nil || f != math.Trunc(f) {
				return 0, fmt.Errorf("%s must be an integer", k)
			}
			i = int64(f)
		}
		n = i
	case float64:
		if x != math.Trunc(x) {
			return 0, fmt.Errorf("%s must be an integer", k)
		}
		n = int64(x)
	case int:
		n = int64(x)
	case int64:
		n = x
	default:
		return 0, fmt.Errorf("%s must be an integer", k)
	}
	return int(min(max(n, int64(lo)), int64(hi))), nil
}
