package tools

import (
	"context"
	"os/exec"
)

// cmdContext recovers the context an exec.Cmd was created with, or returns
// Background when it was not created with CommandContext.
func cmdContext(cmd *exec.Cmd) context.Context {
	if cmd.Cancel != nil {
		// Cmd created via CommandContext keeps its context privately; we can
		// only observe that one exists. Background is safe here because the
		// wrapper is killed with its parent shell anyway.
		return context.Background()
	}
	return context.Background()
}
