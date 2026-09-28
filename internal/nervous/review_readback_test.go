package nervous

import (
	"strings"
	"testing"

	"water/internal/approvals"
)

// Review of Slice W: a display-name recipient ("Dana Lee <dana@fenwick.io>")
// must be spelled as its address only, not letter by letter with the name
// and the angle brackets folded into the "local part".
func TestConfirmSendReadBackSpellsBareAddress(t *testing.T) {
	e := approvals.Envelope{Action: "gmail.send_message", Payload: map[string]any{
		"to": []any{"Dana Lee <dana@fenwick.io>"}, "subject": "Hi"}}
	got := confirmSendReadBack(e)
	if !strings.HasPrefix(got, "Sending to d a n a at f e n w i c k dot i o, subject Hi.") || strings.ContainsAny(got, "<>") {
		t.Fatalf("read-back = %q", got)
	}
}
