// Package config loads non-secret settings from a JSON file and secrets from
// environment variables. Secrets are never read from (or written to) disk by
// this package.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Target selects where worklogs are submitted.
const (
	TargetMock = "mock"
	TargetReal = "real"
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
		Jira:            JiraConfig{BaseURL: "https://application.jira.elementlogic.no"},
		MeetingIssueKey: "EDB-9071",
		LeaveIssueKey:   "EDB-9070",
		WorkdayHours:    7,
		GitHub:          GitHubConfig{APIBaseURL: "https://api.github.com"},
		MockJiraPort:    8099,
		WebPort:         8080,
		Target:          TargetMock,
	}
}

// Load reads the JSON config at path (may be empty to use defaults only) and
// then overlays secrets from the environment.
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
	cfg.JiraAPIToken = os.Getenv(EnvJiraToken)
	cfg.GitHubToken = os.Getenv(EnvGitHubToken)
	if cfg.Target == "" {
		cfg.Target = TargetMock
	}
	return cfg, nil
}

// Validate checks that the fields required for building day plans are present.
// It does not require real-Jira/GitHub secrets, since the MVP can run entirely
// against the mock server.
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
	if c.Target != TargetMock && c.Target != TargetReal {
		missing = append(missing, `target ("mock" or "real")`)
	}
	if len(missing) > 0 {
		return fmt.Errorf("invalid config: %s", strings.Join(missing, ", "))
	}
	return nil
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
