package backend

import (
	"errors"
	"fmt"
	"strings"
)

// ErrAttachmentUnsupported marks an attachment kind a backend cannot deliver
// to its model. Refusing is the fix for a silent drop: the model used to answer
// "I can't access the attached PDF" while the command exited 0.
var ErrAttachmentUnsupported = errors.New("attachment cannot be delivered by this backend")

func refuseUnsupported(backendName string, atts []Attachment, can map[string]bool) error {
	var bad []string
	for _, a := range atts {
		if !can[a.Kind] {
			bad = append(bad, fmt.Sprintf("%s (%s)", a.Name, a.MediaType))
		}
	}
	if len(bad) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %s cannot read %s; use --backend claude-subscription (or /backend in chat), which reads PDFs and images", ErrAttachmentUnsupported, backendName, strings.Join(bad, ", "))
}
