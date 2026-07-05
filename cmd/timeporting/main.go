// Command timeporting is the main entry point for the timereporting assistant.
// It builds day plans for a date range, then serves a local web UI to review,
// edit, and approve worklogs before submitting them to Jira.
package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/kwkgaya/timereporting-assistant/internal/activity"
	"github.com/kwkgaya/timereporting-assistant/internal/config"
	"github.com/kwkgaya/timereporting-assistant/internal/engine"
	"github.com/kwkgaya/timereporting-assistant/internal/ics"
	"github.com/kwkgaya/timereporting-assistant/internal/jira"
	"github.com/kwkgaya/timereporting-assistant/internal/model"
	"github.com/kwkgaya/timereporting-assistant/internal/web"
)

func main() {
	cfgPath := flag.String("config", "config.json", "path to config JSON file")
	from := flag.String("from", "", "start date YYYY-MM-DD (default: first day of current month)")
	to := flag.String("to", "", "end date YYYY-MM-DD (default: last day of current month)")
	targetFlag := flag.String("target", "", "override target: mock or real")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil && !os.IsNotExist(err) {
		log.Fatalf("config: %v", err)
	}
	if *targetFlag != "" {
		cfg.Target = *targetFlag
	}
	if err := cfg.Validate(); err != nil {
		log.Fatalf("config validation: %v", err)
	}

	if cfg.Target == config.TargetReal {
		if err := cfg.RequireRealJira(); err != nil {
			log.Fatalf("%v", err)
		}
		fmt.Println("⚠️  TARGET = REAL JIRA. Worklogs will be written to your actual timesheet.")
	} else {
		fmt.Printf("Target = mock Jira (http://localhost:%d)\n", cfg.MockJiraPort)
	}

	// Build date range.
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

	// --- Jira client (read existing worklogs) ---
	var jiraBaseURL string
	if cfg.Target == config.TargetReal {
		jiraBaseURL = cfg.Jira.BaseURL
	} else {
		jiraBaseURL = fmt.Sprintf("http://localhost:%d", cfg.MockJiraPort)
	}
	jiraClient := jira.NewClient(jiraBaseURL, cfg.Jira.Email, cfg.JiraAPIToken)

	// Read existing worklogs from Jira (or mock) for the whole range.
	existingByDay, err := jiraClient.ExistingWorklogsByDay(cfg.Jira.Email, startDate, endDate)
	if err != nil {
		log.Printf("warning: could not read existing worklogs: %v", err)
		existingByDay = map[string][]model.Worklog{}
	}

	// --- ICS meetings ---
	var allMeetings []model.Meeting
	if cfg.ICSPath != "" {
		allMeetings, err = ics.ParseFile(cfg.ICSPath)
		if err != nil {
			log.Printf("warning: could not parse ICS file %q: %v", cfg.ICSPath, err)
		} else {
			fmt.Printf("Loaded %d calendar events from %s\n", len(allMeetings), cfg.ICSPath)
		}
	}

	// --- Activity collectors ---
	gitCollector := activity.NewGitCollector(cfg.LocalRepos, cfg.GitAuthors)
	var ghCollector *activity.GitHubCollector
	if cfg.GitHub.Username != "" {
		ghCollector = activity.NewGitHubCollector(cfg.GitHub.APIBaseURL, cfg.GitHub.Username, cfg.GitHubToken)
	}

	// --- Build day plans ---
	engCfg := engine.DefaultConfig(cfg.WorkdayHours, cfg.MeetingIssueKey, cfg.LeaveIssueKey)
	plans := make([]model.DayPlan, 0, len(days))
	for _, day := range days {
		dayKey := day.Format("2006-01-02")

		// Existing worklogs for this day.
		existing := existingByDay[dayKey]

		// Meeting minutes.
		meetingMins := ics.TotalMinutesForDay(allMeetings, day)

		// GitHub + local git activity.
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

	// --- Start web review UI ---
	webSrv := web.New(plans, jiraClient, cfg.Target, cfg.WebPort)
	addr := fmt.Sprintf("localhost:%d", cfg.WebPort)
	fmt.Printf("\n✅ Review UI ready → http://%s\n", addr)
	fmt.Printf("   Approve a day in the UI to submit to %s.\n", cfg.Target)
	if cfg.Target == config.TargetMock {
		fmt.Printf("   Mock Jira inspect page → http://localhost:%d\n", cfg.MockJiraPort)
	}

	if err := http.ListenAndServe(addr, webSrv.Handler()); err != nil {
		log.Fatal(err)
	}
}
