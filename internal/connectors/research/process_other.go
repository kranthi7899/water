//go:build !unix

package research

import "os/exec"

func containProcessGroup(*exec.Cmd) {}
