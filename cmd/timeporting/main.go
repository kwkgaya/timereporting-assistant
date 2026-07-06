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
	"github.com/kwkgaya/timereporting-assistant/internal/applog"
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
	cfgPath := fs.String("config", defaultConfigPath(), "path to config JSON file")
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
	cfgPath := fs.String("config", defaultConfigPath(), "path to config JSON file")
	_ = fs.Parse(os.Args[2:])

	cfg, _ := config.Load(*cfgPath)
	if err := setup.RunCredentialSetup(cfg.Jira.BaseURL, cfg.Jira.Email); err != nil {
		log.Fatalf("credentials: %v", err)
	}
}

// runMain is the primary review-and-submit flow.
func runMain() {
	defer applog.Setup("timeporting")()

	cfgPath := flag.String("config", defaultConfigPath(), "path to config JSON file")
	from := flag.String("from", "", "start date YYYY-MM-DD (default: first day of current month)")
	to := flag.String("to", "", "end date YYYY-MM-DD (default: last day of current month)")
	targetFlag := flag.String("target", "", "override target: mock | mock-write | real")
	noBrowser := flag.Bool("no-browser", false, "do not auto-open the browser (used when launched by the tray app)")
	flag.Parse()

	// ── Load config (no CLI wizard — use the web settings page for first-run) ──
	cfg, err := config.Load(*cfgPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatalf("config: %v", err)
	}
	// If config is missing, start with defaults and let the web settings page
	// handle onboarding (it will redirect / to /settings automatically).
	if _, statErr := os.Stat(*cfgPath); os.IsNotExist(statErr) {
		fmt.Printf("No config found at %s — open http://localhost:%d/settings to configure.\n",
			*cfgPath, cfg.WebPort)
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
		// Use the resolved API base (api.atlassian.com/ex/jira/{cloudId} for scoped
		// tokens, or the domain URL for classic tokens).
		realClient = jira.NewClient(cfg.JiraAPIBase(), cfg.Jira.Email, cfg.JiraAPIToken)
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
	_ = readClient // readClient is now handled inside buildPlans

	// ── Build day plans (also used by the /api/reload endpoint) ──────────────
	buildPlans := func(c config.Config, progress web.ProgressFunc) ([]model.DayPlan, *jira.Client, *jira.Client, error) {
		report := func(done, total int, phase string) {
			if progress != nil {
				progress(done, total, phase)
			}
		}
		total := len(days)
		mb := jira.NewClient(fmt.Sprintf("http://localhost:%d", c.MockJiraPort), "", "")
		var rb *jira.Client
		if c.Jira.BaseURL != "" && c.JiraAPIToken != "" {
			rb = jira.NewClient(c.JiraAPIBase(), c.Jira.Email, c.JiraAPIToken)
		}

		var rc *jira.Client // read client
		switch c.Target {
		case config.TargetReal, config.TargetMockWrite:
			rc = rb
		}
		if rc == nil {
			rc = mb
		}

		report(0, total, "Reading existing worklogs…")
		// Fetch from the real-Jira read client and tag source.
		existing := map[string][]model.Worklog{}
		if rc != mb {
			// Real Jira read
			if ex, err := rc.ExistingWorklogsByDay(c.Jira.Email, startDate, endDate); err == nil {
				for dk, wls := range ex {
					for i := range wls {
						wls[i].Source = "real"
					}
					existing[dk] = append(existing[dk], wls...)
				}
			}
		}
		// Always also read from mock so submitted-to-mock worklogs are visible.
		if ex, err := mb.ExistingWorklogsByDay("", startDate, endDate); err == nil {
			for dk, wls := range ex {
				for i := range wls {
					wls[i].Source = "mock"
				}
				existing[dk] = append(existing[dk], wls...)
			}
		}

		var meetings []model.Meeting
		if c.ICSPath != "" {
			report(0, total, "Reading calendar…")
			meetings, _ = ics.ParseFile(c.ICSPath)
		}

		gc := activity.NewGitCollector(c.LocalRepos, c.GitAuthors)
		var ghc *activity.GitHubCollector
		if c.GitHub.Username != "" {
			ghc = activity.NewGitHubCollector(c.GitHub.APIBaseURL, c.GitHub.Username, c.GitHubToken)
		}

		ec := engine.DefaultConfig(c.WorkdayHours, c.MeetingIssueKey, c.LeaveIssueKey)
		var ps []model.DayPlan
		for i, day := range days {
			dk := day.Format("2006-01-02")
			report(i, total, "Scanning activity for "+dk+"…")
			ex := existing[dk]
			mm := ics.TotalMinutesForDay(meetings, day)
			var acts []model.Activity
			la, _ := gc.CollectForDay(day)
			acts = append(acts, la...)
			if ghc != nil {
				ga, _ := ghc.CollectForDay(day)
				acts = append(acts, ga...)
			}
			ps = append(ps, engine.BuildDayPlan(ec, day, model.StatusWorking, ex, mm, acts))
		}
		report(total, total, "Finalizing…")
		return ps, mb, rb, nil
	}

	plans, mockClient, realClient, _ := buildPlans(cfg, nil)

	// buildDay builds a single day on demand (for out-of-range date navigation).
	buildDay := func(c config.Config, day time.Time) (model.DayPlan, error) {
		mb := jira.NewClient(fmt.Sprintf("http://localhost:%d", c.MockJiraPort), "", "")
		var rb *jira.Client
		if c.Jira.BaseURL != "" && c.JiraAPIToken != "" {
			rb = jira.NewClient(c.JiraAPIBase(), c.Jira.Email, c.JiraAPIToken)
		}
		var rc *jira.Client
		switch c.Target {
		case config.TargetReal, config.TargetMockWrite:
			rc = rb
		}
		if rc == nil {
			rc = mb
		}
		existing := map[string][]model.Worklog{}
		if rc != mb {
			if ex, err := rc.ExistingWorklogsByDay(c.Jira.Email, day, day); err == nil {
				for dk, wls := range ex {
					for i := range wls {
						wls[i].Source = "real"
					}
					existing[dk] = append(existing[dk], wls...)
				}
			}
		}
		if ex, err := mb.ExistingWorklogsByDay("", day, day); err == nil {
			for dk, wls := range ex {
				for i := range wls {
					wls[i].Source = "mock"
				}
				existing[dk] = append(existing[dk], wls...)
			}
		}
		var meetings []model.Meeting
		if c.ICSPath != "" {
			meetings, _ = ics.ParseFile(c.ICSPath)
		}
		gc := activity.NewGitCollector(c.LocalRepos, c.GitAuthors)
		var ghc *activity.GitHubCollector
		if c.GitHub.Username != "" {
			ghc = activity.NewGitHubCollector(c.GitHub.APIBaseURL, c.GitHub.Username, c.GitHubToken)
		}
		ec := engine.DefaultConfig(c.WorkdayHours, c.MeetingIssueKey, c.LeaveIssueKey)
		dk := day.Format("2006-01-02")
		ex := existing[dk]
		mm := ics.TotalMinutesForDay(meetings, day)
		var acts []model.Activity
		la, _ := gc.CollectForDay(day)
		acts = append(acts, la...)
		if ghc != nil {
			ga, _ := ghc.CollectForDay(day)
			acts = append(acts, ga...)
		}
		return engine.BuildDayPlan(ec, day, model.StatusWorking, ex, mm, acts), nil
	}

	// ── Web review UI ──────────────────────────────────────────────────────
	webSrv := web.New(plans, mockClient, realClient, cfg.Target, cfg.WebPort).
		WithConfig(cfg, *cfgPath).
		WithPlanBuilder(web.PlanBuilder(buildPlans)).
		WithDayBuilder(web.DayBuilder(buildDay))
	addr := fmt.Sprintf("localhost:%d", cfg.WebPort)
	fmt.Printf("\n✅ Review UI ready → http://%s\n", addr)
	fmt.Printf("   Read from:  %s\n", readLabel(cfg.Target))
	fmt.Printf("   Writing to: %s\n", writeLabel(cfg.Target, cfg.MockJiraPort))
	if cfg.Target != config.TargetReal {
		fmt.Printf("   Mock Jira inspect → http://localhost:%d\n", cfg.MockJiraPort)
	}

	// Open the review UI in the browser shortly after the server starts.
	// Skipped when --no-browser is set (tray app opens it instead).
	if !*noBrowser {
		go func() {
			time.Sleep(700 * time.Millisecond)
			openURL("http://" + addr)
		}()
	}

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
	hideWindow(cmd)
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

// defaultConfigPath returns the platform-appropriate default location for
// config.json. On Windows this is %LOCALAPPDATA%\timereporting-assistant\config.json;
// on other platforms ~/.config/timereporting-assistant/config.json.
//
// If an old-style config.json exists next to the binary (from a previous
// install), it is migrated to the new location automatically.
func defaultConfigPath() string {
	dir := appDataDir()
	_ = os.MkdirAll(dir, 0o700)
	newPath := filepath.Join(dir, "config.json")

	// Migrate old config.json next to the binary, if it exists and the new
	// location is empty.
	if _, err := os.Stat(newPath); os.IsNotExist(err) {
		exe, err2 := os.Executable()
		if err2 == nil {
			old := filepath.Join(filepath.Dir(exe), "config.json")
			if data, err3 := os.ReadFile(old); err3 == nil {
				if err4 := os.WriteFile(newPath, data, 0o600); err4 == nil {
					_ = os.Remove(old) // remove from install dir after migration
					fmt.Printf("Migrated config from %s to %s\n", old, newPath)
				}
			}
		}
	}
	return newPath
}

// appDataDir returns the OS-appropriate user application data directory.
func appDataDir() string {
	switch runtime.GOOS {
	case "windows":
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			return filepath.Join(local, "timereporting-assistant")
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "timereporting-assistant")
	}
	return "."
}
