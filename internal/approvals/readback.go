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
// Every payload key is shown, in full (whitespace and control characters
// collapsed): nothing the approval binds is hidden from the approver.
func ReadBack(e Envelope) string {
	return summary(e, true) + " " + prompt(e.Action)
}

// summary renders e's payload. full renders every value uncut (ReadBack,
// right before yes/no); otherwise long values are shortened for a list,
// but always with a visible note of how much is not shown. Either way,
// every key appears: the known fields of a special-cased action first,
// then any other key under "Also".
func summary(e Envelope, full bool) string {
	p := e.Payload
	lim := func(v any, n int) string {
		if full {
			n = -1
		}
		return clip(text(v), n)
	}
	var s string
	var used []string
	switch actionKey(e.Action) {
	case "twinlink.send_message":
		// Another twin is another party's agent, outside this daemon: the
		// read-back says so, and names the twin, the kind of message and
		// the full text, never a short name that could read as an email.
		s = fmt.Sprintf("Send a %s to twin '%s' (another party's agent, outside this daemon), subject '%s'",
			lim(p["type"], 20), lim(p["to_twin"], 64), lim(p["subject"], 80))
		if v, ok := p["in_reply_to"]; ok && text(v) != "" {
			s += ", answering their request " + lim(v, 40)
		}
		body := ". Message: '%s'."
		if !full {
			body = ". Message begins: '%s'."
		}
		s += fmt.Sprintf(body, lim(p["payload"], 80))
		used = []string{"to_twin", "type", "subject", "in_reply_to", "payload"}
	case "send_email":
		body := "Body: '%s'."
		if !full {
			body = "Body begins: '%s'."
		}
		s = fmt.Sprintf("Send email to %s, subject '%s'. "+body, recipients(e), lim(p["subject"], 80), lim(p["body"], 80))
		used = []string{"to", "subject", "body"}
	case "send_message":
		body := "Body: '%s'."
		if !full {
			body = "Body begins: '%s'."
		}
		s = fmt.Sprintf("Send email to %s, subject '%s'. "+body, recipients(e), lim(p["subject"], 80), lim(p["body"], 80))
		used = []string{"to", "subject", "body"}
	case "draft_message":
		body := "Body: '%s'."
		if !full {
			body = "Body begins: '%s'."
		}
		s = fmt.Sprintf("Draft an email to %s, subject '%s'. "+body, recipients(e), lim(p["subject"], 80), lim(p["body"], 80))
		used = []string{"to", "subject", "body"}
	case "draft_for_review":
		body := "Body: '%s'."
		if !full {
			body = "Body begins: '%s'."
		}
		s = fmt.Sprintf("Prepare a draft in your own Gmail for you to review and send yourself: to %s, subject '%s'. "+body, recipients(e), lim(p["subject"], 80), lim(p["body"], 80))
		used = []string{"to", "subject", "body"}
	case "move_event":
		s = fmt.Sprintf("Move event %s to start %s", lim(p["event_id"], 80), lim(p["new_start"], 40))
		if _, ok := p["new_end"]; ok {
			s += " ending " + lim(p["new_end"], 40)
		}
		s += "."
		used = []string{"event_id", "new_start", "new_end"}
	case "create_event":
		s = fmt.Sprintf("Create event '%s' starting %s", lim(p["title"], 80), lim(p["start"], 40))
		if _, ok := p["end"]; ok {
			s += " ending " + lim(p["end"], 40)
		}
		if who := list(p["attendees"]); who != "" {
			s += " with " + who
		}
		s += "."
		used = []string{"title", "start", "end", "attendees"}
	default:
		parts := rest(p, nil, lim, " ")
		if len(parts) == 0 {
			return fmt.Sprintf("Run %s.", e.Action)
		}
		return fmt.Sprintf("Run %s with %s.", e.Action, strings.Join(parts, "; "))
	}
	if extra := rest(p, used, lim, ": "); len(extra) > 0 {
		s += " Also " + strings.Join(extra, "; ") + "."
	}
	return s
}

// rest renders every key of p not in used, sorted, as "key<sep>value".
func rest(p map[string]any, used []string, lim func(any, int) string, sep string) []string {
	skip := make(map[string]bool, len(used))
	for _, k := range used {
		skip[k] = true
	}
	keys := make([]string, 0, len(p))
	for k := range p {
		if !skip[k] {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = clip(k, -1) + sep + lim(p[k], 60)
	}
	return parts
}

func prompt(action string) string {
	switch actionKey(action) {
	case "twinlink.send_message":
		return "Say yes to send it to the other twin or no to cancel."
	case "send_email", "send_message":
		return "Say yes to send or no to cancel."
	case "draft_message", "draft_for_review":
		return "Say yes to create the draft or no to cancel."
	case "create_event":
		return "Say yes to create it or no to cancel."
	case "move_event":
		return "Say yes to move it or no to cancel."
	}
	return "Say yes to proceed or no to cancel."
}

// recipients always shows the addresses that will receive the mail; a
// display name is only ever a label next to the address it belongs to.
func recipients(e Envelope) string {
	to := listItems(e.Payload["to"])
	for i := range to {
		// An address is never shortened, but it can never carry a newline
		// or control character into the read-back either.
		to[i] = clip(to[i], -1)
	}
	if len(to) == 1 && e.Recipient != "" && !strings.Contains(e.Recipient, to[0]) {
		return clip(e.Recipient, 60) + " <" + to[0] + ">"
	}
	if len(to) == 0 {
		return "nobody"
	}
	return strings.Join(to, ", ")
}

// actionKey is what a read-back is chosen by: the full id for a twinlink
// action (its send_message must never be read back as an email), the short
// function name for everything else.
func actionKey(action string) string {
	if strings.HasPrefix(action, "twinlink.") {
		return action
	}
	return shortName(action)
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

// list joins v's items, each collapsed so no item can inject a line.
func list(v any) string {
	items := listItems(v)
	for i := range items {
		items[i] = clip(items[i], -1)
	}
	return strings.Join(items, ", ")
}

// clip collapses whitespace and control characters, then, when n >= 0,
// cuts to about n runes and says how many were left out. A negative n
// never cuts.
func clip(s string, n int) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	r := []rune(s)
	if n < 0 || len(r) <= n {
		return s
	}
	cut := string(r[:n])
	if r[n] != ' ' {
		if i := strings.LastIndex(cut, " "); i > len(cut)/2 {
			cut = cut[:i]
		}
	}
	cut = strings.TrimSpace(cut)
	return fmt.Sprintf("%s… [+%d chars not shown]", cut, len(r)-len([]rune(cut)))
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
		fmt.Fprintf(&b, "  %d. %s [risk %s, expires %s]\n", i+1, summary(e, false), risk, e.ExpiresAt.Local().Format("15:04"))
	}
	b.WriteString("Enter a number to review it, then answer yes or no.")
	return b.String()
}

// Respond decides an envelope from a free-text or transcribed reply.
func (q *Queue) Respond(ctx context.Context, id, reply string) (Envelope, error) {
	return q.Decide(ctx, id, Match(reply))
}
