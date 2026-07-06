// Package config loads non-secret settings from a JSON file and secrets from
// environment variables. Secrets are never read from (or written to) disk by
// this package.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/kwkgaya/timereporting-assistant/internal/keychain"
)

// DefaultPath returns the canonical location of config.json for the current
// user: %LOCALAPPDATA%\timereporting-assistant\config.json on Windows, or
// ~/.config/timereporting-assistant/config.json elsewhere. All binaries use
// this so they read and write the same persisted configuration.
func DefaultPath() string {
	var dir string
	if runtime.GOOS == "windows" {
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			dir = filepath.Join(local, "timereporting-assistant")
		}
	}
	if dir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".config", "timereporting-assistant")
		} else {
			dir = "."
		}
	}
	return filepath.Join(dir, "config.json")
}

// Target selects where worklogs are submitted.
const (
	TargetMock      = "mock"
	TargetMockWrite = "mock-write" // read from Jira, write to mock
	TargetJira      = "real"
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
	// APIBase is the resolved base URL for REST API calls. For scoped tokens
	// this is https://api.atlassian.com/ex/jira/{cloudId}; for classic tokens
	// it matches BaseURL. Set automatically during token validation.
	APIBase string `json:"apiBase,omitempty"`
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
	// ICSUrl is a published Outlook calendar URL (ICS format). When set, the
	// app fetches the live calendar from this URL instead of reading ICSPath.
	ICSUrl          string       `json:"icsUrl"`
	MockJiraPort    int          `json:"mockJiraPort"`
	WebPort         int          `json:"webPort"`
	Target          string       `json:"target"`

	// LogMeetingsSeparately creates one worklog per calendar meeting (using
	// the meeting title as the comment) instead of a single aggregate worklog.
	// Defaults to true.
	LogMeetingsSeparately bool `json:"logMeetingsSeparately"`

	// AutoUpdate enables checking GitHub Releases on startup and installing
	// newer versions automatically. Defaults to true (see Default()).
	AutoUpdate bool `json:"autoUpdate"`
	// UpdatePrerelease allows auto-update to install prerelease (beta) versions.
	// Defaults to false.
	UpdatePrerelease bool `json:"updatePrerelease"`

	// Secrets, populated from the environment (never from JSON).
	JiraAPIToken string `json:"-"`
	GitHubToken  string `json:"-"`
}

// Default returns a config with sensible defaults for the MVP.
func Default() Config {
	return Config{
		Jira:                  JiraConfig{},
		MeetingIssueKey:       "EDB-9071",
		LeaveIssueKey:         "EDB-9070",
		WorkdayHours:          7,
		GitHub:                GitHubConfig{APIBaseURL: "https://api.github.com"},
		MockJiraPort:          9099,
		WebPort:               9080,
		Target:                TargetJira,
		AutoUpdate:            true,
		UpdatePrerelease:      false,
		LogMeetingsSeparately: true,
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

	// Self-heal: an on-disk config may have blank required scalar fields (for
	// example, left over from an earlier partial-save bug). Empty means "not
	// set", so fall back to the built-in defaults instead of failing
	// validation and refusing to start.
	def := Default()
	if cfg.MeetingIssueKey == "" {
		cfg.MeetingIssueKey = def.MeetingIssueKey
	}
	if cfg.LeaveIssueKey == "" {
		cfg.LeaveIssueKey = def.LeaveIssueKey
	}
	if cfg.WorkdayHours <= 0 {
		cfg.WorkdayHours = def.WorkdayHours
	}
	if cfg.MockJiraPort == 0 {
		cfg.MockJiraPort = def.MockJiraPort
	}
	if cfg.WebPort == 0 {
		cfg.WebPort = def.WebPort
	}
	if cfg.GitHub.APIBaseURL == "" {
		cfg.GitHub.APIBaseURL = def.GitHub.APIBaseURL
	}
	if cfg.Target == "" {
		cfg.Target = TargetJira
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
	if c.Target != TargetMock && c.Target != TargetMockWrite && c.Target != TargetJira {
		missing = append(missing, `target ("mock", "mock-write", or "real")`)
	}
	if len(missing) > 0 {
		return fmt.Errorf("invalid config: %s", strings.Join(missing, ", "))
	}
	return nil
}

// NeedsJiraRead returns true when the target requires reading from Jira.
func (c Config) NeedsJiraRead() bool {
	return c.Target == TargetMockWrite || c.Target == TargetJira
}

// NeedsJiraWrite returns true when the target writes to Jira.
func (c Config) NeedsJiraWrite() bool {
	return c.Target == TargetJira
}

// JiraAPIBase returns the base URL to use for Jira REST API calls.
// If APIBase has been resolved (scoped token), it returns that; otherwise BaseURL.
func (c Config) JiraAPIBase() string {
	if c.Jira.APIBase != "" {
		return c.Jira.APIBase
	}
	return c.Jira.BaseURL
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
