package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kwkgaya/timereporting-assistant/internal/keychain"
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
	// Secrets come from the keychain first, then env. Only assert the env
	// fallback when the machine running the test has no stored credential
	// (otherwise the real keychain entry legitimately takes precedence).
	if _, err := keychain.Load(keychain.JiraTarget); err != nil {
		if cfg.JiraAPIToken != "tok-jira" {
			t.Errorf("Jira secret not loaded from env: %q", cfg.JiraAPIToken)
		}
	}
	if _, err := keychain.Load(keychain.GitHubTarget); err != nil {
		if cfg.GitHubToken != "tok-gh" {
			t.Errorf("GitHub secret not loaded from env: %q", cfg.GitHubToken)
		}
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

// A config file with blank required scalar fields (e.g. left over from an
// earlier partial-save bug) must be self-healed to the defaults on load so the
// app still passes validation and starts.
func TestLoadSelfHealsBlankRequiredFields(t *testing.T) {
	t.Setenv(EnvJiraToken, "")
	t.Setenv(EnvGitHubToken, "")

	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	content := `{
      "jira": {"baseUrl": "https://example.atlassian.net", "email": "a@b.c"},
      "meetingIssueKey": "",
      "leaveIssueKey": "",
      "workdayHours": 0,
      "webPort": 0,
      "mockJiraPort": 0,
      "target": ""
    }`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("healed config should be valid: %v", err)
	}
	if cfg.MeetingIssueKey != "EDB-9071" || cfg.LeaveIssueKey != "EDB-9070" {
		t.Errorf("keys not healed: %q / %q", cfg.MeetingIssueKey, cfg.LeaveIssueKey)
	}
	if cfg.WorkdayHours != 7 {
		t.Errorf("workday hours not healed: %v", cfg.WorkdayHours)
	}
	if cfg.WebPort != 9080 || cfg.MockJiraPort != 9099 {
		t.Errorf("ports not healed: web=%d mock=%d", cfg.WebPort, cfg.MockJiraPort)
	}
	if cfg.Target != TargetMock {
		t.Errorf("target not healed: %q", cfg.Target)
	}
	// Real values from the file must be preserved.
	if cfg.Jira.BaseURL != "https://example.atlassian.net" {
		t.Errorf("baseURL clobbered: %q", cfg.Jira.BaseURL)
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
