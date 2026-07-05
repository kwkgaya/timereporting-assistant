// Package activity collects work signals from GitHub (via the REST API) and
// from local git repositories (via the git binary), normalising them into
// model.Activity values so the rest of the pipeline doesn't care about the
// source.
package activity

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/kwkgaya/timereporting-assistant/internal/model"
)

// Source tag constants used in model.Activity.Source.
const (
	SourceGitHubCommit = "github-commit"
	SourceGitHubPR     = "github-pr"
	SourceGitHubReview = "github-review"
	SourceLocalGit     = "local-git"
)

// GitHubCollector fetches activity from the GitHub REST v3 API.
type GitHubCollector struct {
	apiBase  string
	username string
	token    string
	http     *http.Client
}

// NewGitHubCollector creates a collector. apiBase is normally
// "https://api.github.com". token may be empty for public repos but is
// required to read private repos and to avoid rate limits.
func NewGitHubCollector(apiBase, username, token string) *GitHubCollector {
	return &GitHubCollector{
		apiBase:  strings.TrimRight(apiBase, "/"),
		username: username,
		token:    token,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

func (g *GitHubCollector) get(path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, g.apiBase+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github GET %s: %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

// CollectForDay returns the user's GitHub activity (commits on PRs, PR
// open/merge events, PR review submissions) on a specific UTC day.
func (g *GitHubCollector) CollectForDay(day time.Time) ([]model.Activity, error) {
	dayStr := day.UTC().Format("2006-01-02")
	from := dayStr + "T00:00:00Z"
	to := dayStr + "T23:59:59Z"

	var acts []model.Activity

	// PRs created or merged by the user on this day.
	prs, err := g.searchPRs(fmt.Sprintf("author:%s created:%s..%s", g.username, from, to))
	if err == nil {
		acts = append(acts, prs...)
	}
	merged, err := g.searchPRs(fmt.Sprintf("author:%s merged:%s..%s", g.username, from, to))
	if err == nil {
		acts = append(acts, merged...)
	}

	// PR reviews submitted on this day.
	reviews, err := g.searchReviews(dayStr)
	if err == nil {
		acts = append(acts, reviews...)
	}

	return dedupe(acts), nil
}

type prItem struct {
	HTMLURL string `json:"html_url"`
	Title   string `json:"title"`
	Head    struct {
		Ref string `json:"ref"` // branch name
	} `json:"head"`
	CreatedAt string `json:"created_at"`
}

func (g *GitHubCollector) searchPRs(query string) ([]model.Activity, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("per_page", "100")
	q.Set("type", "pr")
	var result struct {
		Items []prItem `json:"items"`
	}
	if err := g.get("/search/issues?"+q.Encode(), &result); err != nil {
		return nil, err
	}
	day := model.Day(time.Now().UTC()) // will be overridden per-item below
	_ = day
	var acts []model.Activity
	for _, pr := range result.Items {
		t, _ := time.Parse(time.RFC3339, pr.CreatedAt)
		acts = append(acts, model.Activity{
			Date:   model.Day(t),
			Source: SourceGitHubPR,
			Text:   pr.Title,
			Ref:    pr.Head.Ref + " " + pr.HTMLURL,
		})
	}
	return acts, nil
}

func (g *GitHubCollector) searchReviews(dayStr string) ([]model.Activity, error) {
	q := url.Values{}
	q.Set("q", fmt.Sprintf("reviewed-by:%s updated:%s", g.username, dayStr))
	q.Set("per_page", "100")
	q.Set("type", "pr")
	var result struct {
		Items []struct {
			HTMLURL   string `json:"html_url"`
			Title     string `json:"title"`
			UpdatedAt string `json:"updated_at"`
		} `json:"items"`
	}
	if err := g.get("/search/issues?"+q.Encode(), &result); err != nil {
		return nil, err
	}
	var acts []model.Activity
	for _, pr := range result.Items {
		t, _ := time.Parse(time.RFC3339, pr.UpdatedAt)
		acts = append(acts, model.Activity{
			Date:   model.Day(t),
			Source: SourceGitHubReview,
			Text:   "Review: " + pr.Title,
			Ref:    pr.HTMLURL,
		})
	}
	return acts, nil
}

// dedupe removes duplicate activities by Ref+Source.
func dedupe(acts []model.Activity) []model.Activity {
	seen := map[string]bool{}
	out := make([]model.Activity, 0, len(acts))
	for _, a := range acts {
		key := a.Source + "|" + a.Ref
		if !seen[key] {
			seen[key] = true
			out = append(out, a)
		}
	}
	return out
}

// GitCollector scans local git repositories.
type GitCollector struct {
	repoPaths []string
	authors   []string // git author emails to match
	gitBin    string   // path to git binary (defaults to "git")
}

// NewGitCollector creates a collector for the given repo folders and author
// emails. authors may be empty to collect from all authors.
func NewGitCollector(repoPaths, authors []string) *GitCollector {
	return &GitCollector{repoPaths: repoPaths, authors: authors, gitBin: "git"}
}

// CollectForDay returns commit activity from all configured local repos for
// the given UTC day, reading from all branches.
func (g *GitCollector) CollectForDay(day time.Time) ([]model.Activity, error) {
	after := day.UTC().Format("2006-01-02") + " 00:00:00"
	before := day.UTC().Format("2006-01-02") + " 23:59:59"

	var all []model.Activity
	for _, repo := range g.repoPaths {
		acts, err := g.collectRepo(repo, after, before)
		if err != nil {
			// Don't fail if one repo can't be read (may not be a git repo).
			continue
		}
		all = append(all, acts...)
	}
	return dedupe(all), nil
}

func (g *GitCollector) collectRepo(repoPath, after, before string) ([]model.Activity, error) {
	args := []string{
		"-C", repoPath,
		"log",
		"--all",
		"--no-merges",
		"--format=%H%x1F%ae%x1F%s%x1F%D", // hash, author email, subject, decorations (branch refs)
		"--after=" + after,
		"--before=" + before,
	}
	for _, author := range g.authors {
		args = append(args, "--author="+author)
	}

	cmd := exec.Command(g.gitBin, args...)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	var acts []model.Activity
	day := model.Day(mustParse(after))
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1f", 4)
		if len(parts) < 3 {
			continue
		}
		hash := parts[0][:min(7, len(parts[0]))]
		subject := parts[2]
		ref := hash
		if len(parts) == 4 && parts[3] != "" {
			ref = parts[3] + " " + hash
		}
		acts = append(acts, model.Activity{
			Date:   day,
			Source: SourceLocalGit,
			Text:   subject,
			Ref:    repoPath + " " + ref,
		})
	}
	return acts, nil
}

func mustParse(s string) time.Time {
	t, _ := time.Parse("2006-01-02 15:04:05", s)
	return t
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
