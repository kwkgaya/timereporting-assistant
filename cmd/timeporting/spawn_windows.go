//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// hideWindow sets CREATE_NO_WINDOW on cmd so no console window is allocated
// when spawning child processes from a GUI or tray application.
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
