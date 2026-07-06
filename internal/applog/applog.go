// Package applog sets up file-based logging for the GUI-subsystem binaries.
//
// All binaries are built with "-H windowsgui", which means they have no
// console: anything written to stdout/stderr is discarded. This package
// redirects stdout, stderr, and the standard log package to a shared rotating
// log file under the user's application-data directory so failures can be
// diagnosed.
package applog

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
)

// maxLogBytes is the size at which the log file is truncated on the next start.
const maxLogBytes = 5 << 20 // 5 MiB

// LogDir returns the directory where log files are written.
func LogDir() string {
	return filepath.Join(appDataDir(), "logs")
}

// LogPath returns the full path to the shared log file.
func LogPath() string {
	return filepath.Join(LogDir(), "timeporting.log")
}

// Setup redirects stdout, stderr, and the standard logger to the shared log
// file and tags each line with the given component name. It returns a cleanup
// function that closes the file. On any failure it falls back to the original
// stdout/stderr and returns a no-op cleanup.
func Setup(component string) func() {
	dir := LogDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return func() {}
	}
	path := LogPath()

	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogBytes {
		flags = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0o600)
	if err != nil {
		return func() {}
	}

	os.Stdout = f
	os.Stderr = f
	log.SetOutput(f)
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[" + component + "] ")
	log.Printf("=== %s started (pid %d) ===", component, os.Getpid())
	return func() {
		log.Printf("=== %s exiting (pid %d) ===", component, os.Getpid())
		_ = f.Close()
	}
}

// appDataDir returns the OS-appropriate user application-data directory. It
// mirrors the logic used for the config file location.
func appDataDir() string {
	if runtime.GOOS == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			return filepath.Join(local, "timereporting-assistant")
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "timereporting-assistant")
	}
	return "."
}
