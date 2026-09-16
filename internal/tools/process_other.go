//go:build !unix

package tools

import "os/exec"

// Shell authorization is refused on these platforms until confinement exists.
func containProcess(cmd *exec.Cmd) {}
