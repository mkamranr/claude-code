//go:build !windows

package desktop

import "os/exec"

func hideWindow(*exec.Cmd) {}

func killTree(cmd *exec.Cmd) { cmd.Process.Kill() }
