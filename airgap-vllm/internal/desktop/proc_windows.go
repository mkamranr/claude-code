package desktop

import (
	"os/exec"
	"strconv"
	"syscall"
)

const createNoWindow = 0x08000000

// hideWindow stops claude.exe (a console program) from flashing a console
// window when started from the GUI process.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
}

// killTree kills claude.exe and anything it started (a Bash tool's shell, a
// build it ran), which Process.Kill alone would leave running.
func killTree(cmd *exec.Cmd) {
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(cmd.Process.Pid))
	hideWindow(kill)
	if err := kill.Run(); err != nil {
		cmd.Process.Kill()
	}
}
