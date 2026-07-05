// Package config loads non-secret settings from a JSON file and secrets from
// environment variables. Secrets are never read from (or written to) disk by
// this package.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/kwkgaya/timereporting-assistant/internal/keychain"
)

// Target selects where worklogs are submitted.
const (
	TargetMock      = "mock"
	TargetMockWrite = "mock-write" // read from real Jira, write to mock
	TargetReal      = "real"
)

// Environment variable names for secrets.
const (
	EnvJiraToken   = "JIRA_API_TOKEN"
	EnvGitHubToken = "GITHUB_TOKEN"
)

// JiraConfig holds Jira Cloud connection settings (non-secret).
type JiraConfig struct {
	BaseURL string `json:"baseUrl"`
	Email   string `json:"email"`
}

// GitHubConfig holds GitHub connection settings (non-secret).
type GitHubConfig struct {
	APIBaseURL string `json:"apiBaseUrl"`
	Username   string `json:"username"`
}

// Config is the full application configuration.
type Config struct {
	Jira            JiraConfig   `json:"jira"`
	MeetingIssueKey string       `json:"meetingIssueKey"`
	LeaveIssueKey   string       `json:"leaveIssueKey"`
	WorkdayHours    float64      `json:"workdayHours"`
	GitHub          GitHubConfig `json:"github"`
	LocalRepos      []string     `json:"localRepos"`
	GitAuthors      []string     `json:"gitAuthors"`
	ICSPath         string       `json:"icsPath"`
	MockJiraPort    int          `json:"mockJiraPort"`
	WebPort         int          `json:"webPort"`
	Target          string       `json:"target"`

	// Secrets, populated from the environment (never from JSON).
	JiraAPIToken string `json:"-"`
	GitHubToken  string `json:"-"`
}

// Default returns a config with sensible defaults for the MVP.
func Default() Config {
	return Config{
		Jira:            JiraConfig{},
		MeetingIssueKey: "EDB-9071",
		LeaveIssueKey:   "EDB-9070",
		WorkdayHours:    7,
		GitHub:          GitHubConfig{APIBaseURL: "https://api.github.com"},
		MockJiraPort:    9099,
		WebPort:         9080,
		Target:          TargetMock,
	}
}

// Load reads the JSON config at path (may be empty to use defaults only),
// then overlays secrets from the OS keychain (Windows Credential Manager),
// falling back to environment variables when the keychain has no entry.
func Load(path string) (Config, error) {
	cfg := Default()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return cfg, fmt.Errorf("read config %q: %w", path, err)
		}
		if err := json.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("parse config %q: %w", path, err)
		}
	}

	// Jira credentials: keychain → env var.
	if cred, err := keychain.Load(keychain.JiraTarget); err == nil {
		if cfg.Jira.Email == "" {
			cfg.Jira.Email = cred.Username
		}
		cfg.JiraAPIToken = cred.Secret
	} else if !errors.Is(err, keychain.ErrNotFound) {
		// Unexpected error reading keychain — fall through to env var silently.
		cfg.JiraAPIToken = os.Getenv(EnvJiraToken)
	} else {
		cfg.JiraAPIToken = os.Getenv(EnvJiraToken)
	}

	// GitHub token: keychain → env var.
	if cred, err := keychain.Load(keychain.GitHubTarget); err == nil {
		cfg.GitHubToken = cred.Secret
	} else {
		cfg.GitHubToken = os.Getenv(EnvGitHubToken)
	}

	if cfg.Target == "" {
		cfg.Target = TargetMock
	}
	return cfg, nil
}

// Validate checks that the fields required for building day plans are present.
func (c Config) Validate() error {
	var missing []string
	if c.MeetingIssueKey == "" {
		missing = append(missing, "meetingIssueKey")
	}
	if c.LeaveIssueKey == "" {
		missing = append(missing, "leaveIssueKey")
	}
	if c.WorkdayHours <= 0 {
		missing = append(missing, "workdayHours (> 0)")
	}
	if c.Target != TargetMock && c.Target != TargetMockWrite && c.Target != TargetReal {
		missing = append(missing, `target ("mock", "mock-write", or "real")`)
	}
	if len(missing) > 0 {
		return fmt.Errorf("invalid config: %s", strings.Join(missing, ", "))
	}
	return nil
}

// NeedsRealJiraRead returns true when the target requires reading from real Jira.
func (c Config) NeedsRealJiraRead() bool {
	return c.Target == TargetMockWrite || c.Target == TargetReal
}

// NeedsRealJiraWrite returns true when the target writes to real Jira.
func (c Config) NeedsRealJiraWrite() bool {
	return c.Target == TargetReal
}

// RequireRealJira validates the settings needed to talk to real Jira.
func (c Config) RequireRealJira() error {
	var missing []string
	if c.Jira.BaseURL == "" {
		missing = append(missing, "jira.baseUrl")
	}
	if c.Jira.Email == "" {
		missing = append(missing, "jira.email")
	}
	if c.JiraAPIToken == "" {
		missing = append(missing, EnvJiraToken+" (env)")
	}
	if len(missing) > 0 {
		return fmt.Errorf("real Jira access requires: %s", strings.Join(missing, ", "))
	}
	return nil
}
