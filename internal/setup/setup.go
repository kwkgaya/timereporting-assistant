// Package setup implements interactive first-run and credential setup flows.
package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/kwkgaya/timereporting-assistant/internal/config"
	"github.com/kwkgaya/timereporting-assistant/internal/keychain"
)

// RunConfigWizard interactively gathers all non-secret config settings and
// writes config.json. Existing values are shown as defaults.
func RunConfigWizard(cfgPath string, existing config.Config) (config.Config, error) {
	fmt.Println()
	fmt.Println("┌─────────────────────────────────────────────────┐")
	fmt.Println("│   Timereporting Assistant — First-run setup     │")
	fmt.Println("└─────────────────────────────────────────────────┘")
	fmt.Println("Press Enter to accept a default shown in [brackets].")
	fmt.Println()

	cfg := existing

	// Jira base URL.
	cfg.Jira.BaseURL = prompt("Jira base URL", cfg.Jira.BaseURL, "https://company.atlassian.net")
	cfg.Jira.Email = prompt("Your Jira login email", cfg.Jira.Email, "")
	cfg.MeetingIssueKey = prompt("Meeting task key", cfg.MeetingIssueKey, "EDB-9071")
	cfg.LeaveIssueKey = prompt("Leave/holiday task key", cfg.LeaveIssueKey, "EDB-9070")

	hoursStr := prompt("Working hours per day", fmt.Sprintf("%.0f", cfg.WorkdayHours), "7")
	if h := parseFloat(hoursStr); h > 0 {
		cfg.WorkdayHours = h
	}

	// Local git repos (one per line until blank).
	fmt.Printf("\nLocal git repo paths (one per line, blank to finish):\n")
	if len(cfg.LocalRepos) > 0 {
		fmt.Printf("  Currently: %s\n", strings.Join(cfg.LocalRepos, ", "))
		if promptBool("Keep existing repo list?", true) {
			// keep as is
		} else {
			cfg.LocalRepos = nil
		}
	}
	if len(cfg.LocalRepos) == 0 {
		for {
			p := prompt("  Path", "", "")
			if p == "" {
				break
			}
			if _, err := os.Stat(p); err != nil {
				fmt.Printf("  ⚠ Path does not exist: %s\n", p)
			}
			cfg.LocalRepos = append(cfg.LocalRepos, p)
		}
	}

	// Git author emails — detect from git config.
	defaultEmail := detectGitEmail()
	if len(cfg.GitAuthors) == 0 && defaultEmail != "" {
		cfg.GitAuthors = []string{defaultEmail}
	}
	authorStr := prompt("Your git author email(s) (comma-separated)", strings.Join(cfg.GitAuthors, ","), defaultEmail)
	cfg.GitAuthors = splitTrim(authorStr, ",")

	// GitHub.
	cfg.GitHub.Username = prompt("Work GitHub username (blank to skip)", cfg.GitHub.Username, "")
	if cfg.GitHub.APIBaseURL == "" {
		cfg.GitHub.APIBaseURL = "https://api.github.com"
	}

	// ICS file.
	cfg.ICSPath = prompt("Path to .ics calendar export (blank to skip)", cfg.ICSPath, "")

	// Ports.
	cfg.MockJiraPort = promptInt("Mock Jira port", cfg.MockJiraPort, 8099)
	cfg.WebPort = promptInt("Review UI port", cfg.WebPort, 8080)

	// Summary + confirm.
	fmt.Println()
	fmt.Println("─── Configuration summary ───────────────────────")
	fmt.Printf("  Jira URL:        %s\n", cfg.Jira.BaseURL)
	fmt.Printf("  Jira email:      %s\n", cfg.Jira.Email)
	fmt.Printf("  Meeting task:    %s\n", cfg.MeetingIssueKey)
	fmt.Printf("  Leave task:      %s\n", cfg.LeaveIssueKey)
	fmt.Printf("  Workday hours:   %.0f\n", cfg.WorkdayHours)
	fmt.Printf("  Git repos:       %s\n", strings.Join(cfg.LocalRepos, ", "))
	fmt.Printf("  Git authors:     %s\n", strings.Join(cfg.GitAuthors, ", "))
	fmt.Printf("  GitHub user:     %s\n", cfg.GitHub.Username)
	fmt.Printf("  ICS path:        %s\n", cfg.ICSPath)
	fmt.Println("─────────────────────────────────────────────────")

	if !promptBool("Save to "+cfgPath+"?", true) {
		return cfg, fmt.Errorf("setup cancelled by user")
	}
	if err := writeConfig(cfgPath, cfg); err != nil {
		return cfg, err
	}
	fmt.Printf("✅ Config saved to %s\n", cfgPath)
	return cfg, nil
}

// RunCredentialSetup guides the user through creating a scoped Jira API token
// and storing it in the OS keychain. It validates the token against the Jira
// API before storing.
func RunCredentialSetup(baseURL, email string) error {
	fmt.Println()
	fmt.Println("┌─────────────────────────────────────────────────┐")
	fmt.Println("│   Jira API token setup                          │")
	fmt.Println("└─────────────────────────────────────────────────┘")

	fmt.Println()
	fmt.Println("You need a SCOPED Jira API token (not a classic token).")
	fmt.Println("It only requires two permissions: read:jira-work and write:jira-work.")
	fmt.Println()
	fmt.Println("Steps to create one:")
	fmt.Println("  1. Open: https://id.atlassian.com/manage-profile/security/api-tokens")
	fmt.Println(`  2. Click "Create API token" -> choose "API token with scopes"`)
	fmt.Println("  3. Name it: timereporting-assistant")
	fmt.Println("  4. Add scopes: ✓ read:jira-work   ✓ write:jira-work")
	fmt.Println("  5. Click Create → COPY the token (shown only once)")
	fmt.Println()

	if promptBool("Open the Atlassian token page in your browser now?", true) {
		openBrowser("https://id.atlassian.com/manage-profile/security/api-tokens")
		fmt.Println("  Browser opened. Come back here once you have copied the token.")
		fmt.Println()
	}

	// Gather email if not provided.
	if email == "" {
		email = prompt("Your Jira email address", "", "")
	}

	for attempt := 1; attempt <= 3; attempt++ {
		token, err := readSecret("Paste your scoped API token")
		if err != nil || token == "" {
			return fmt.Errorf("no token provided")
		}
		fmt.Print("  Validating token… ")
		if err := validateJiraToken(baseURL, email, token); err != nil {
			fmt.Printf("❌ Invalid: %v\n", err)
			if attempt < 3 {
				fmt.Println("  Please try again.")
			}
			continue
		}
		fmt.Println("✅ Valid!")

		if err := keychain.Store(keychain.JiraTarget, email, token); err != nil {
			fmt.Printf("⚠ Could not save to keychain: %v\n  Set env var %s manually.\n", err, config.EnvJiraToken)
			return nil
		}
		fmt.Println("✅ Credentials saved to Windows Credential Manager.")
		fmt.Println("  You won't be prompted again on this machine.")
		return nil
	}
	return fmt.Errorf("too many failed attempts")
}

// RunGitHubTokenSetup guides the user through providing a GitHub PAT.
func RunGitHubTokenSetup() error {
	fmt.Println()
	fmt.Println("To fetch your GitHub activity, provide a GitHub Personal Access Token.")
	fmt.Println("Scopes needed: repo (read)  — or use a fine-grained token with Contents:read.")
	fmt.Println()
	fmt.Println("Create one at: https://github.com/settings/tokens")
	fmt.Println()
	if promptBool("Open GitHub token page in browser?", true) {
		openBrowser("https://github.com/settings/tokens")
	}
	token, err := readSecret("Paste your GitHub token")
	if err != nil || token == "" {
		return fmt.Errorf("no token provided")
	}
	if err := keychain.Store(keychain.GitHubTarget, "", token); err != nil {
		fmt.Printf("⚠ Could not save to keychain: %v\n  Set env var %s manually.\n", err, config.EnvGitHubToken)
		return nil
	}
	fmt.Println("✅ GitHub token saved.")
	return nil
}

// EnsureCredentials checks for required credentials and triggers the setup
// flow interactively if they are missing. Returns an error only if setup fails
// or the user cancels.
func EnsureCredentials(cfg *config.Config, needReal bool) error {
	if !needReal {
		return nil
	}
	if cfg.JiraAPIToken != "" {
		return nil
	}
	fmt.Println("\n⚠ No Jira credentials found.")
	return RunCredentialSetup(cfg.Jira.BaseURL, cfg.Jira.Email)
}

// ── helpers ──────────────────────────────────────────────────────────────────

func prompt(label, current, defaultVal string) string {
	shown := current
	if shown == "" {
		shown = defaultVal
	}
	if shown != "" {
		fmt.Printf("%s [%s]: ", label, shown)
	} else {
		fmt.Printf("%s: ", label)
	}
	var line string
	fmt.Scanln(&line)
	line = strings.TrimSpace(line)
	if line == "" {
		return shown
	}
	return line
}

func promptBool(label string, def bool) bool {
	defStr := "Y/n"
	if !def {
		defStr = "y/N"
	}
	fmt.Printf("%s [%s]: ", label, defStr)
	var line string
	fmt.Scanln(&line)
	line = strings.ToLower(strings.TrimSpace(line))
	if line == "" {
		return def
	}
	return line == "y" || line == "yes"
}

func promptInt(label string, current, defaultVal int) int {
	shown := current
	if shown == 0 {
		shown = defaultVal
	}
	fmt.Printf("%s [%d]: ", label, shown)
	var n int
	if _, err := fmt.Scan(&n); err != nil || n <= 0 {
		return shown
	}
	return n
}

func parseFloat(s string) float64 {
	var f float64
	fmt.Sscanf(s, "%f", &f)
	return f
}

func splitTrim(s, sep string) []string {
	parts := strings.Split(s, sep)
	var out []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func detectGitEmail() string {
	out, err := exec.Command("git", "config", "--global", "user.email").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// readSecret reads a password/token from the terminal without echoing.
func readSecret(label string) (string, error) {
	fmt.Printf("%s (hidden): ", label)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		return string(b), err
	}
	// Fallback for non-terminal (e.g. piped input in tests).
	var line string
	_, err := fmt.Scanln(&line)
	return strings.TrimSpace(line), err
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func validateJiraToken(baseURL, email, token string) error {
	req, err := http.NewRequest(http.MethodGet, baseURL+"/rest/api/3/myself", nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(email, token)
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized {
		return errors.New("token rejected (401 Unauthorized) — check email and token")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

func writeConfig(path string, cfg config.Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
