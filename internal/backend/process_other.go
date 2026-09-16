//go:build !unix

package backend

import "os/exec"

func containProcessGroup(*exec.Cmd) {}
