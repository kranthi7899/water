package tools

import "strings"

// Drain the process continuously, but retain at most 64 KiB. Truncating only
// after CombinedOutput allowed a noisy command to consume unbounded memory.
type limitedOutput struct {
	text      strings.Builder
	truncated bool
}

func (b *limitedOutput) Write(p []byte) (int, error) {
	n := len(p)
	keep := min(n, 64*1024-b.text.Len())
	if keep > 0 {
		b.text.Write(p[:keep])
	}
	if keep < n {
		b.truncated = true
	}
	return n, nil
}
func (b *limitedOutput) String() string {
	if b.truncated {
		return b.text.String() + "\n[truncated]"
	}
	return b.text.String()
}
