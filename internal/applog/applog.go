// Package applog sets up file-based logging for the GUI-subsystem binaries.
//
// All binaries are built with "-H windowsgui", which means they have no
// console: anything written to stdout/stderr is discarded. This package
// redirects stdout, stderr, and the standard log package to a shared rotating
// log file under the user's application-data directory so failures can be
// diagnosed.
package applog

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
)

// maxLogBytes is the size at which the log file is truncated on the next start.
const maxLogBytes = 5 << 20 // 5 MiB

// tokenPattern matches Atlassian API token shapes and generic bearer/basic
// credential fragments so they are never written to the log file.
var tokenPattern = regexp.MustCompile(
	`(?i)(ATATT[A-Za-z0-9+/=_-]{20,}` + // Atlassian scoped token prefix
		`|ATCTT[A-Za-z0-9+/=_-]{20,}` + // Atlassian classic token prefix
		`|ghp_[A-Za-z0-9]{30,}` + // GitHub PAT (classic)
		`|github_pat_[A-Za-z0-9_]{30,}` + // GitHub PAT (fine-grained)
		`|Bearer\s+[A-Za-z0-9._~+/-]{20,}` + // Bearer token in header logs
		`|:[A-Za-z0-9+/]{30,}@)`, // Basic-auth password in URL
)

// scrubWriter wraps an io.Writer and masks credential patterns before writing.
type scrubWriter struct{ w io.Writer }

func (s scrubWriter) Write(p []byte) (int, error) {
	scrubbed := tokenPattern.ReplaceAll(p, []byte("[REDACTED]"))
	n, err := s.w.Write(scrubbed)
	// Return the original length so callers don't think a short write occurred.
	if n == len(scrubbed) {
		n = len(p)
	}
	return n, err
}

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

	sw := scrubWriter{f}
	os.Stdout = f
	os.Stderr = f
	log.SetOutput(sw)
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
