//go:build !windows

package activity

import "os/exec"

// hideCmd is a no-op on non-Windows platforms.
func hideCmd(_ *exec.Cmd) {}
