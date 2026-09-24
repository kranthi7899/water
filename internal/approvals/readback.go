package approvals

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

// ReadBack is what the CEO hears or reads before deciding. It is built by
// code from the structured payload, the same payload the hash binds, so
// what is read back is exactly what would execute. No model writes it.
func ReadBack(e Envelope) string {
	return summary(e) + " " + prompt(e.Action)
}

func summary(e Envelope) string {
	p := e.Payload
	switch shortName(e.Action) {
	case "send_email":
		return fmt.Sprintf("Send email to %s, subject '%s'. Body begins: '%s'.", recipients(e), clip(text(p["subject"]), 80), clip(text(p["body"]), 80))
	case "create_event":
		s := fmt.Sprintf("Create event '%s' starting %s", clip(text(p["title"]), 80), clip(text(p["start"]), 40))
		if who := list(p["attendees"]); who != "" {
			s += " with " + who
		}
		return s + "."
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = k + " " + clip(text(p[k]), 60)
	}
	if len(parts) == 0 {
		return fmt.Sprintf("Run %s.", e.Action)
	}
	return fmt.Sprintf("Run %s with %s.", e.Action, strings.Join(parts, "; "))
}

func prompt(action string) string {
	switch shortName(action) {
	case "send_email":
		return "Say yes to send or no to cancel."
	case "create_event":
		return "Say yes to create it or no to cancel."
	}
	return "Say yes to proceed or no to cancel."
}

// recipients always shows the addresses that will receive the mail; a
// display name is only ever a label next to the address it belongs to.
func recipients(e Envelope) string {
	to := listItems(e.Payload["to"])
	if len(to) == 1 && e.Recipient != "" && !strings.Contains(e.Recipient, to[0]) {
		return clip(e.Recipient, 60) + " <" + to[0] + ">"
	}
	if len(to) == 0 {
		return "nobody"
	}
	return strings.Join(to, ", ")
}

func shortName(action string) string {
	if i := strings.LastIndex(action, "."); i >= 0 {
		return action[i+1:]
	}
	return action
}

func text(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []any, []string:
		return list(t)
	}
	return fmt.Sprint(v)
}

func listItems(v any) []string {
	var out []string
	switch t := v.(type) {
	case []any:
		for _, x := range t {
			out = append(out, text(x))
		}
	case []string:
		out = append(out, t...)
	case string:
		out = []string{t}
	}
	return out
}

func list(v any) string { return strings.Join(listItems(v), ", ") }

// clip collapses whitespace and control characters, then cuts to n runes.
func clip(s string, n int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := string(r[:n])
	if r[n] != ' ' {
		if i := strings.LastIndex(cut, " "); i > len(cut)/2 {
			cut = cut[:i]
		}
	}
	return strings.TrimSpace(cut) + "…"
}

// Menu renders pending envelopes as a numbered list for the CLI.
func Menu(envs []Envelope) string {
	if len(envs) == 0 {
		return "No approvals waiting."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Approvals waiting (%d):\n", len(envs))
	for i, e := range envs {
		risk := e.Risk
		if risk == "" {
			risk = "unrated"
		}
		fmt.Fprintf(&b, "  %d. %s [risk %s, expires %s]\n", i+1, summary(e), risk, e.ExpiresAt.Local().Format("15:04"))
	}
	b.WriteString("Enter a number to review it, then answer yes or no.")
	return b.String()
}

// Respond decides an envelope from a free-text or transcribed reply.
func (q *Queue) Respond(ctx context.Context, id, reply string) (Envelope, error) {
	return q.Decide(ctx, id, Match(reply))
}
