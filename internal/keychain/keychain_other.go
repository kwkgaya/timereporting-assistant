//go:build !windows

package keychain

import (
	"os"
	"strings"
)

// On non-Windows platforms credentials come from environment variables.
// JIRA: JIRA_EMAIL + JIRA_API_TOKEN
// GITHUB: GITHUB_TOKEN
func store(target, username, secret string) error {
	// Cannot write env vars programmatically; instruct the user.
	return nil // no-op; user must set env vars manually
}

func load(target string) (Credential, error) {
	switch {
	case strings.HasSuffix(target, "/jira"):
		email := os.Getenv("JIRA_EMAIL")
		token := os.Getenv("JIRA_API_TOKEN")
		if token == "" {
			return Credential{}, ErrNotFound
		}
		return Credential{Username: email, Secret: token}, nil
	case strings.HasSuffix(target, "/github"):
		token := os.Getenv("GITHUB_TOKEN")
		if token == "" {
			return Credential{}, ErrNotFound
		}
		return Credential{Username: "", Secret: token}, nil
	}
	return Credential{}, ErrNotFound
}

func delete_(target string) error {
	return nil // no-op on non-Windows
}
