//go:build windows

package activity

import (
	"os/exec"
	"syscall"
)

// hideCmd sets CREATE_NO_WINDOW so spawning git.exe from a GUI-subsystem
// process does not flash a console window.
func hideCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000}
}
