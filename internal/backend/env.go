package backend

import (
	"os"
	"sort"
	"strings"
)

// MeteredKeyVars are environment variables that, if exported, can cause a
// subscription CLI to silently bill a metered API key instead of the
// subscription. They are stripped from every subprocess environment and
// reported by `water doctor`.
var MeteredKeyVars = []string{
	"ANTHROPIC_API_KEY",
	"ANTHROPIC_AUTH_TOKEN",
	"OPENAI_API_KEY",
	"CLAUDE_API_KEY",
}

// LeakedKeys returns the names of metered credential variables currently
// exported in this process's environment (values are never returned).
func LeakedKeys() []string {
	var out []string
	for _, k := range MeteredKeyVars {
		if v, ok := os.LookupEnv(k); ok && strings.TrimSpace(v) != "" {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ScrubbedEnv returns a copy of the process environment with every metered
// credential variable removed. Subscription backends MUST launch their
// subprocess with this environment so an exported API key can never outrank
// the subscription login.
func ScrubbedEnv() []string {
	deny := map[string]bool{}
	for _, k := range MeteredKeyVars {
		deny[k] = true
	}
	var out []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if deny[name] {
			continue
		}
		out = append(out, kv)
	}
	return out
}
