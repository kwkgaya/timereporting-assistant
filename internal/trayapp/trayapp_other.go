//go:build !windows

// Package trayapp is a Windows-only component. On other platforms all
// functions are no-ops so the rest of the codebase can still compile.
package trayapp

import "fmt"

// Run is a no-op on non-Windows platforms.
func Run(version, cfgPath string) {
	fmt.Println("tray app is Windows-only; exiting.")
}

// RegisterAutoStart is a no-op on non-Windows platforms.
func RegisterAutoStart() error { return nil }

// UnregisterAutoStart is a no-op on non-Windows platforms.
func UnregisterAutoStart() error { return nil }
