// Package keychain abstracts OS credential storage. On Windows it uses the
// Windows Credential Manager (wincred); on other platforms it falls back to
// environment variables and warns the user.
package keychain

import "errors"

// ErrNotFound is returned when no credential exists for the given target.
var ErrNotFound = errors.New("credential not found")

// Credential holds a username and a secret.
type Credential struct {
	Username string
	Secret   string
}

// Store saves a credential under the given target name.
func Store(target, username, secret string) error {
	return store(target, username, secret)
}

// Load retrieves a credential by target name. Returns ErrNotFound if not set.
func Load(target string) (Credential, error) {
	return load(target)
}

// Delete removes a stored credential.
func Delete(target string) error {
	return delete_(target)
}

const JiraTarget = "timereporting-assistant/jira"
const GitHubTarget = "timereporting-assistant/github"
