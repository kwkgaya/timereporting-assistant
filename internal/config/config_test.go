package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsWhenNoPath(t *testing.T) {
	t.Setenv(EnvJiraToken, "")
	t.Setenv(EnvGitHubToken, "")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\") error: %v", err)
	}
	if cfg.Target != TargetMock {
		t.Errorf("default target = %q, want %q", cfg.Target, TargetMock)
	}
	if cfg.MeetingIssueKey != "EDB-9071" {
		t.Errorf("meeting key = %q, want EDB-9071", cfg.MeetingIssueKey)
	}
	if cfg.LeaveIssueKey != "EDB-9070" {
		t.Errorf("leave key = %q, want EDB-9070", cfg.LeaveIssueKey)
	}
	if cfg.WorkdayHours != 7 {
		t.Errorf("workday hours = %v, want 7", cfg.WorkdayHours)
	}
}

func TestLoadFileOverridesAndEnvSecrets(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	content := `{
      "jira": {"baseUrl": "https://example.atlassian.net", "email": "a@b.c"},
      "meetingIssueKey": "MEET-1",
      "leaveIssueKey": "LEAVE-1",
      "workdayHours": 8,
      "target": "real"
    }`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvJiraToken, "tok-jira")
	t.Setenv(EnvGitHubToken, "tok-gh")

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if cfg.Jira.BaseURL != "https://example.atlassian.net" {
		t.Errorf("baseURL = %q", cfg.Jira.BaseURL)
	}
	if cfg.MeetingIssueKey != "MEET-1" || cfg.LeaveIssueKey != "LEAVE-1" {
		t.Errorf("keys not overridden: %q / %q", cfg.MeetingIssueKey, cfg.LeaveIssueKey)
	}
	if cfg.WorkdayHours != 8 {
		t.Errorf("workday hours = %v, want 8", cfg.WorkdayHours)
	}
	if cfg.Target != TargetReal {
		t.Errorf("target = %q, want real", cfg.Target)
	}
	if cfg.JiraAPIToken != "tok-jira" || cfg.GitHubToken != "tok-gh" {
		t.Errorf("secrets not loaded from env: %q / %q", cfg.JiraAPIToken, cfg.GitHubToken)
	}
}

func TestValidate(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config should be valid: %v", err)
	}
	bad := Default()
	bad.MeetingIssueKey = ""
	bad.WorkdayHours = 0
	if err := bad.Validate(); err == nil {
		t.Error("expected validation error for empty meeting key / zero hours")
	}
}

func TestRequireRealJira(t *testing.T) {
	cfg := Default()
	cfg.Jira.BaseURL = "https://x.atlassian.net"
	cfg.Jira.Email = "a@b.c"
	cfg.JiraAPIToken = ""
	if err := cfg.RequireRealJira(); err == nil {
		t.Error("expected error when token missing")
	}
	cfg.JiraAPIToken = "tok"
	if err := cfg.RequireRealJira(); err != nil {
		t.Errorf("expected valid real Jira config: %v", err)
	}
}
