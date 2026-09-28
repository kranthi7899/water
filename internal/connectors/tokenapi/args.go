package tokenapi

import "strings"

// ArgString returns args[k] trimmed, or "" — the same helper gapi.ArgString
// provides for the Google connectors.
func ArgString(args map[string]any, k string) string {
	s, _ := args[k].(string)
	return strings.TrimSpace(s)
}
