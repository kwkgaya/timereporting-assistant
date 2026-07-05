// Command timeporting is the main entry point for the timereporting assistant.
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/kwkgaya/timereporting-assistant/internal/activity"
	"github.com/kwkgaya/timereporting-assistant/internal/config"
	"github.com/kwkgaya/timereporting-assistant/internal/engine"
	"github.com/kwkgaya/timereporting-assistant/internal/ics"
	"github.com/kwkgaya/timereporting-assistant/internal/jira"
	"github.com/kwkgaya/timereporting-assistant/internal/model"
	"github.com/kwkgaya/timereporting-assistant/internal/setup"
	"github.com/kwkgaya/timereporting-assistant/internal/web"
)

// version is set at build time via -ldflags "-X main.version=vX.Y.Z".
var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "configure":
			runConfigure()
			return
		case "credentials":
			runCredentials()
			return
		case "version", "--version", "-v":
			fmt.Printf("timereporting-assistant %s\n", version)
			return
		}
	}
	runMain()
}

// runConfigure runs the interactive first-run config wizard.
func runConfigure() {
	fs := flag.NewFlagSet("configure", flag.ExitOnError)
	cfgPath := fs.String("config", "config.json", "path to config JSON file")
	_ = fs.Parse(os.Args[2:])

	existing, err := config.Load(*cfgPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatalf("load config: %v", err)
	}
	if _, err := setup.RunConfigWizard(*cfgPath, existing); err != nil {
		log.Fatalf("configure: %v", err)
	}
}

// runCredentials runs the Jira credential setup flow.
func runCredentials() {
	fs := flag.NewFlagSet("credentials", flag.ExitOnError)
	cfgPath := fs.String("config", "config.json", "path to config JSON file")
	_ = fs.Parse(os.Args[2:])

	cfg, _ := config.Load(*cfgPath)
	if err := setup.RunCredentialSetup(cfg.Jira.BaseURL, cfg.Jira.Email); err != nil {
		log.Fatalf("credentials: %v", err)
	}
}

// runMain is the primary review-and-submit flow.
func runMain() {
	cfgPath := flag.String("config", "config.json", "path to config JSON file")
	from := flag.String("from", "", "start date YYYY-MM-DD (default: first day of current month)")
	to := flag.String("to", "", "end date YYYY-MM-DD (default: last day of current month)")
	targetFlag := flag.String("target", "", "override target: mock | mock-write | real")
	flag.Parse()

	// ── Load config (first-run wizard if missing) ──────────────────────────
	cfg, err := config.Load(*cfgPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatalf("config: %v", err)
	}
	if _, statErr := os.Stat(*cfgPath); os.IsNotExist(statErr) {
		fmt.Printf("No config file found at %s — starting setup wizard.\n", *cfgPath)
		cfg, err = setup.RunConfigWizard(*cfgPath, cfg)
		if err != nil {
			log.Fatalf("setup: %v", err)
		}
		// Reload to pick up saved values.
		cfg, err = config.Load(*cfgPath)
		if err != nil {
			log.Fatalf("reload config: %v", err)
		}
	}
	if *targetFlag != "" {
		cfg.Target = *targetFlag
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config validation: %v", err)
	}

	// ── Credential setup if needed ─────────────────────────────────────────
	if cfg.NeedsRealJiraRead() {
		if err := setup.EnsureCredentials(&cfg, true); err != nil {
			log.Fatalf("credential setup: %v", err)
		}
	}

	// ── Target summary ─────────────────────────────────────────────────────
	switch cfg.Target {
	case config.TargetReal:
		fmt.Println("⚠️  TARGET = REAL JIRA. Worklogs will be written to your actual timesheet.")
	case config.TargetMockWrite:
		fmt.Printf("Target = mock-write (reading real Jira, writing to mock http://localhost:%d)\n", cfg.MockJiraPort)
	default:
		fmt.Printf("Target = mock Jira (http://localhost:%d)\n", cfg.MockJiraPort)
	}

	// ── Date range ─────────────────────────────────────────────────────────
	now := time.Now().UTC()
	startDate := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	endDate := time.Date(now.Year(), now.Month()+1, 0, 0, 0, 0, 0, time.UTC)
	if *from != "" {
		startDate, err = time.Parse("2006-01-02", *from)
		if err != nil {
			log.Fatalf("--from: %v", err)
		}
	}
	if *to != "" {
		endDate, err = time.Parse("2006-01-02", *to)
		if err != nil {
			log.Fatalf("--to: %v", err)
		}
	}
	days := model.Weekdays(startDate, endDate)
	if len(days) == 0 {
		log.Fatalf("no weekdays in range %s – %s", startDate.Format("2006-01-02"), endDate.Format("2006-01-02"))
	}
	fmt.Printf("Building plans for %d weekdays (%s – %s)…\n",
		len(days), startDate.Format("2006-01-02"), endDate.Format("2006-01-02"))

	// ── Jira clients (split read/write for mock-write mode) ────────────────
	mockBase := fmt.Sprintf("http://localhost:%d", cfg.MockJiraPort)

	// If any target uses the mock, make sure it's running (auto-spawn the
	// bundled mockjira binary if the port isn't already listening).
	if cfg.Target != config.TargetReal {
		ensureMockRunning(cfg.MockJiraPort)
	}

	var readClient *jira.Client // for fetching existing worklogs
	var mockClient *jira.Client // always writes to the mock
	var realClient *jira.Client // writes to real Jira; nil when no credentials

	mockClient = jira.NewClient(mockBase, "", "")
	if cfg.Jira.BaseURL != "" && cfg.JiraAPIToken != "" {
		realClient = jira.NewClient(cfg.Jira.BaseURL, cfg.Jira.Email, cfg.JiraAPIToken)
	}

	switch cfg.Target {
	case config.TargetReal:
		readClient = realClient
	case config.TargetMockWrite:
		readClient = realClient
	default: // mock
		readClient = mockClient
	}
	if readClient == nil {
		// mock-write/real selected but no creds yet; fall back to mock reads.
		readClient = mockClient
	}

	// ── Read existing worklogs ─────────────────────────────────────────────
	existingByDay, err := readClient.ExistingWorklogsByDay(cfg.Jira.Email, startDate, endDate)
	if err != nil {
		log.Printf("warning: could not read existing worklogs: %v", err)
		existingByDay = map[string][]model.Worklog{}
	}

	// ── ICS meetings ───────────────────────────────────────────────────────
	var allMeetings []model.Meeting
	if cfg.ICSPath != "" {
		allMeetings, err = ics.ParseFile(cfg.ICSPath)
		if err != nil {
			log.Printf("warning: could not parse ICS file %q: %v", cfg.ICSPath, err)
		} else {
			fmt.Printf("Loaded %d calendar events from %s\n", len(allMeetings), cfg.ICSPath)
		}
	}

	// ── Activity collectors ────────────────────────────────────────────────
	gitCollector := activity.NewGitCollector(cfg.LocalRepos, cfg.GitAuthors)
	var ghCollector *activity.GitHubCollector
	if cfg.GitHub.Username != "" {
		ghCollector = activity.NewGitHubCollector(cfg.GitHub.APIBaseURL, cfg.GitHub.Username, cfg.GitHubToken)
	}

	// ── Build day plans ────────────────────────────────────────────────────
	engCfg := engine.DefaultConfig(cfg.WorkdayHours, cfg.MeetingIssueKey, cfg.LeaveIssueKey)
	plans := make([]model.DayPlan, 0, len(days))
	for _, day := range days {
		dayKey := day.Format("2006-01-02")
		existing := existingByDay[dayKey]
		meetingMins := ics.TotalMinutesForDay(allMeetings, day)

		var acts []model.Activity
		localActs, _ := gitCollector.CollectForDay(day)
		acts = append(acts, localActs...)
		if ghCollector != nil {
			ghActs, _ := ghCollector.CollectForDay(day)
			acts = append(acts, ghActs...)
		}
		plan := engine.BuildDayPlan(engCfg, day, model.StatusWorking, existing, meetingMins, acts)
		plans = append(plans, plan)
	}

	// ── Web review UI ──────────────────────────────────────────────────────
	webSrv := web.New(plans, mockClient, realClient, cfg.Target, cfg.WebPort)
	addr := fmt.Sprintf("localhost:%d", cfg.WebPort)
	fmt.Printf("\n✅ Review UI ready → http://%s\n", addr)
	fmt.Printf("   Read from:  %s\n", readLabel(cfg.Target))
	fmt.Printf("   Writing to: %s\n", writeLabel(cfg.Target, cfg.MockJiraPort))
	if cfg.Target != config.TargetReal {
		fmt.Printf("   Mock Jira inspect → http://localhost:%d\n", cfg.MockJiraPort)
	}

	// Open the review UI in the browser shortly after the server starts.
	go func() {
		time.Sleep(700 * time.Millisecond)
		openURL("http://" + addr)
	}()

	if err := http.ListenAndServe(addr, webSrv.Handler()); err != nil {
		log.Fatal(err)
	}
}

func readLabel(target string) string {
	switch target {
	case config.TargetReal, config.TargetMockWrite:
		return "Real Jira (read-only)"
	default:
		return "Mock Jira"
	}
}

func writeLabel(target string, mockPort int) string {
	switch target {
	case config.TargetReal:
		return "Real Jira ⚠️"
	default:
		return fmt.Sprintf("Mock Jira (http://localhost:%d)", mockPort)
	}
}

// ensureMockRunning starts the bundled mockjira binary if the given port is not
// already accepting connections. Best-effort: silently gives up on any error.
func ensureMockRunning(port int) {
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	if c, err := net.DialTimeout("tcp", addr, 300*time.Millisecond); err == nil {
		_ = c.Close()
		return // already running
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	name := "mockjira"
	if runtime.GOOS == "windows" {
		name = "mockjira.exe"
	}
	path := filepath.Join(filepath.Dir(exe), name)
	if _, err := os.Stat(path); err != nil {
		return // not bundled alongside this binary
	}
	cmd := exec.Command(path, "-port", fmt.Sprintf("%d", port))
	if err := cmd.Start(); err != nil {
		return
	}
	fmt.Printf("Started bundled mock Jira on port %d\n", port)
	// Wait briefly for it to come up.
	for i := 0; i < 20; i++ {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// openURL opens the given URL in the default browser (best-effort).
func openURL(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
