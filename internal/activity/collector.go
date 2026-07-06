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
	SourceGitHubCommit   = "github-commit"
	SourceGitHubPR       = "github-pr"
	SourceGitHubReview   = "github-review"
	SourceLocalGit       = "local-git"
	SourceLocalGitReflog = "local-git-reflog" // #16: commits only in reflog
)

// ── GitHub collector ─────────────────────────────────────────────────────────

// GitHubCollector fetches activity from the GitHub REST v3 API.
type GitHubCollector struct {
	apiBase  string
	username string
	token    string
	http     *http.Client
}

// NewGitHubCollector creates a collector.
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

// CollectForDay returns the user's GitHub activity on a specific UTC day.
func (g *GitHubCollector) CollectForDay(day time.Time) ([]model.Activity, error) {
	dayStr := day.UTC().Format("2006-01-02")
	from := dayStr + "T00:00:00Z"
	to := dayStr + "T23:59:59Z"

	var acts []model.Activity
	if prs, err := g.searchPRs(fmt.Sprintf("author:%s created:%s..%s", g.username, from, to)); err == nil {
		acts = append(acts, prs...)
	}
	if merged, err := g.searchPRs(fmt.Sprintf("author:%s merged:%s..%s", g.username, from, to)); err == nil {
		acts = append(acts, merged...)
	}
	if reviews, err := g.searchReviews(dayStr); err == nil {
		acts = append(acts, reviews...)
	}
	return dedupe(acts), nil
}

type prItem struct {
	HTMLURL string `json:"html_url"`
	Title   string `json:"title"`
	Head    struct {
		Ref string `json:"ref"`
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

// ── Local git collector ───────────────────────────────────────────────────────

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
// the given UTC day.
//
// Multi-clone dedup (#11): multiple clones of the same repo (same origin URL)
// only contribute each commit hash once, regardless of how many local paths
// host it.
//
// Reflog (#16): after scanning branches (--all), also scans the reflog to pick
// up orphaned/rebased/detached-HEAD commits not reachable from any branch.
func (g *GitCollector) CollectForDay(day time.Time) ([]model.Activity, error) {
	after := day.UTC().Format("2006-01-02") + " 00:00:00"
	before := day.UTC().Format("2006-01-02") + " 23:59:59"

	// Group repo paths by their origin remote URL to detect clones.
	// Repos without a readable origin are treated as independent.
	originGroups := g.groupByOrigin()

	seenHash := map[string]bool{} // global hash dedup across all repos/clones
	var all []model.Activity

	// Process one canonical representative per origin group.
	for _, paths := range originGroups {
		// All paths in the group are clones of the same repo.
		// Collect from all of them but deduplicate by commit hash.
		for _, repo := range paths {
			acts, hashes, err := g.collectRepo(repo, after, before, seenHash)
			if err != nil {
				continue
			}
			for h := range hashes {
				seenHash[h] = true
			}
			all = append(all, acts...)
		}

		// #16: scan reflog of the first accessible path for orphaned commits.
		for _, repo := range paths {
			reflogActs, err := g.collectReflog(repo, after, before, seenHash)
			if err != nil {
				continue
			}
			// Mark reflog hashes as seen so subsequent paths don't re-add them.
			for _, a := range reflogActs {
				if h := extractHash(a.Ref); h != "" {
					seenHash[h] = true
				}
			}
			all = append(all, reflogActs...)
			break // only need reflog from one clone per origin group
		}
	}

	return dedupe(all), nil
}

// groupByOrigin maps origin remote URL → list of repo paths sharing that
// origin. Repos without a readable origin get a unique key (the path itself).
func (g *GitCollector) groupByOrigin() map[string][]string {
	groups := map[string][]string{}
	for _, path := range g.repoPaths {
		origin := g.remoteURL(path)
		if origin == "" {
			origin = "__local__:" + path // treat as unique
		}
		groups[origin] = append(groups[origin], path)
	}
	return groups
}

// remoteURL returns the fetch URL of the "origin" remote, or "" on failure.
func (g *GitCollector) remoteURL(repoPath string) string {
	cmd := exec.Command(g.gitBin, "-C", repoPath, "remote", "get-url", "origin")
	hideCmd(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// collectRepo scans one repo for commits on any branch matching the date range
// and author filters. It returns activities and the set of full commit hashes
// it found. Hashes already present in seenHash are skipped (multi-clone dedup).
func (g *GitCollector) collectRepo(repoPath, after, before string, seenHash map[string]bool) ([]model.Activity, map[string]bool, error) {
	args := []string{
		"-C", repoPath,
		"log",
		"--all",
		"--source",
		"--no-merges",
		"--format=%H%x1F%ae%x1F%s%x1F%S%x1F%aI",
		"--after=" + after,
		"--before=" + before,
	}
	for _, author := range g.authors {
		args = append(args, "--author="+author)
	}

	cmd := exec.Command(g.gitBin, args...)
	hideCmd(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, nil, err
	}

	var acts []model.Activity
	newHashes := map[string]bool{}
	day := model.Day(mustParse(after))

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1f", 5)
		if len(parts) < 3 {
			continue
		}
		fullHash := parts[0]
		if fullHash == "" {
			continue
		}
		if seenHash[fullHash] || newHashes[fullHash] {
			continue // already counted from another clone
		}
		newHashes[fullHash] = true

		subject := parts[2]
		// Build a human-readable ref: repo + branch + commit time (no hash — not useful for Rovo).
		commitTime := ""
		if len(parts) == 5 && parts[4] != "" {
			// Parse ISO date and reformat as "15:04" for brevity.
			if t, err := time.Parse(time.RFC3339, parts[4]); err == nil {
				commitTime = " " + t.Format("15:04")
			}
		}
		ref := ""
		if len(parts) >= 4 && parts[3] != "" {
			ref = parts[3] + commitTime
		} else {
			ref = strings.TrimSpace(commitTime)
		}
		acts = append(acts, model.Activity{
			Date:   day,
			Source: SourceLocalGit,
			Text:   subject,
			Ref:    strings.TrimSpace(ref),
		})
	}
	return acts, newHashes, nil
}

// collectReflog finds commits reachable via the reflog but NOT via --all
// branches (orphaned/rebased/detached-HEAD work). Limited to commits whose
// author date falls in [after, before] and matching the configured authors.
func (g *GitCollector) collectReflog(repoPath, after, before string, seenHash map[string]bool) ([]model.Activity, error) {
	// git log -g: walks the reflog instead of commit ancestry.
	// --format="%H %ae %aI %s" gives full-hash, author-email, ISO date, subject.
	args := []string{
		"-C", repoPath,
		"log", "-g",
		"--no-merges",
		"--format=%H%x1F%ae%x1F%aI%x1F%s",
		"--after=" + after,
		"--before=" + before,
	}
	for _, a := range g.authors {
		args = append(args, "--author="+a)
	}

	cmd := exec.Command(g.gitBin, args...)
	hideCmd(cmd)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	day := model.Day(mustParse(after))
	var acts []model.Activity
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x1f", 4)
		if len(parts) < 4 {
			continue
		}
		fullHash := parts[0]
		if fullHash == "" || len(fullHash) != 40 {
			continue
		}
		// Skip commits already found via --all branches.
		if seenHash[fullHash] {
			continue
		}
		seenHash[fullHash] = true // mark so the same hash from another reflog entry is not re-added

		subject := parts[3]
		// Use the author date/time instead of the hash.
		commitTime := ""
		if t, err := time.Parse(time.RFC3339, parts[2]); err == nil {
			commitTime = t.Format("15:04")
		}
		ref := ""
		if commitTime != "" {
			ref = commitTime + " (reflog)"
		} else {
			ref = "(reflog)"
		}
		acts = append(acts, model.Activity{
			Date:   day,
			Source: SourceLocalGitReflog,
			Text:   subject,
			Ref:    ref,
		})
	}
	return acts, nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

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

// extractHash returns the first 40-char hex token from a Ref string.
func extractHash(ref string) string {
	for _, part := range strings.Fields(ref) {
		if len(part) == 40 {
			allHex := true
			for _, c := range part {
				if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
					allHex = false
					break
				}
			}
			if allHex {
				return part
			}
		}
	}
	return ""
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
