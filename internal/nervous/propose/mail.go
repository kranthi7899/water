package propose

import (
	"context"
	"fmt"
	"strings"

	"water/internal/nervous/intents"
	"water/internal/nervous/slots"
)

var mailReplySpec = intents.FunctionSpec{
	ID:       "mail.reply",
	Args:     map[string]slots.Type{"who": slots.TypePerson, "body": slots.TypeText},
	Required: []string{"who", "body"},
	Class:    intents.ClassAction,
	Emits:    []string{"to", "subject", "body"},
}

// buildMailReply backs BOTH mail.draft_reply (gmail.draft_message, level D)
// and mail.send_reply (gmail.send_message, level A) -- internal/nervous
// picks the target and the approval behavior from whichever intent actually
// matched (Intent.Action / Intent.RequiresApproval), not from anything this
// function decides. It needs the latest message from "who" in the store, to
// build a "Re: <subject>" reply: no such message is Unresolved (never a
// guess at what's being replied to).
func buildMailReply(ctx context.Context, d Deps, a map[string]slots.Value) (Proposal, Outcome, error) {
	who, hasWho := a["who"]
	body, hasBody := a["body"]
	if !hasWho || !hasBody {
		return Proposal{}, Unresolved, nil
	}

	msgs, err := d.Store.MessagesFrom(ctx, who.Person.Email, 1)
	if err != nil {
		return Proposal{}, Ok, fmt.Errorf("propose: mail.reply: %w", err)
	}
	if len(msgs) == 0 {
		return Proposal{}, Unresolved, nil
	}
	latest := msgs[0]

	subject := latest.Subject
	if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(subject)), "re:") {
		subject = "Re: " + subject
	}

	payload := map[string]any{
		"to":      []string{who.Person.Email},
		"subject": subject,
		"body":    body.Text,
	}
	summary := fmt.Sprintf("Reply to %s <%s>, subject '%s': '%s'", who.Person.Name, who.Person.Email, subject, body.Text)
	return Proposal{Payload: payload, Summary: summary, Tainted: latest.External}, Ok, nil
}
