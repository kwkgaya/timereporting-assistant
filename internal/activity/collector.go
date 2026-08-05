// Package activity collects work signals from GitHub (via the REST API) and
// from local git repositories (via the git binary), normalising them into
// model.Activity values so the rest of the pipeline doesn't care about the
// source.
package activity

import (
	"context"
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

// gitTimeout bounds every git invocation. Without it a repository on a stalled
// network share, or a git that stops to prompt for credentials, blocks the day
// build forever and the whole app appears hung.
const gitTimeout = 30 * time.Second

// gitCommand builds a git command that is killed if it outlives gitTimeout.
// The returned cancel func must be called once the command has completed.
func gitCommand(bin string, args ...string) (*exec.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	cmd := exec.CommandContext(ctx, bin, args...)
	// Never let git stop for an interactive credential or SSH prompt.
	cmd.Env = append(cmd.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=echo", "SSH_ASKPASS=echo")
	return cmd, cancel
}

// Source tag constants used in model.Activity.Source.
const (
	SourceGitHubCommit   = "github-commit"
	SourceGitHubPR       = "github-pr"
	SourceGitHubReview   = "github-review"
	SourceGitHubComment  = "github-comment" // #99: PR/issue comments, distinct from formal reviews
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
	return g.getAbs(g.apiBase+path, out)
}

// getAbs is like get but takes a full URL, needed for endpoints (e.g. a PR's
// own API URL) returned by an earlier response rather than built from apiBase.
func (g *GitHubCollector) getAbs(fullURL string, out any) error {
	req, err := http.NewRequest(http.MethodGet, fullURL, nil)
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
		return fmt.Errorf("github GET %s: %d: %s", fullURL, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, out)
}

// headRef fetches the source branch name of a PR via its API URL. The
// /search/issues endpoint (used by searchPRs/searchReviews/searchComments)
// never includes "head", regardless of whether the PR is open or merged, so
// the branch name — where the Jira key usually lives — must be fetched
// separately from the PR's own API URL.
func (g *GitHubCollector) headRef(prAPIURL string) string {
	if prAPIURL == "" {
		return ""
	}
	var pr struct {
		Head struct {
			Ref string `json:"ref"`
		} `json:"head"`
	}
	if err := g.getAbs(prAPIURL, &pr); err != nil {
		return ""
	}
	return pr.Head.Ref
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
	if comments, err := g.searchComments(dayStr); err == nil {
		acts = append(acts, comments...)
	}
	return dedupe(acts), nil
}

// searchItem is the shape shared by every /search/issues result we consume.
// PullRequest is only present when the result is a PR (not a plain issue).
type searchItem struct {
	HTMLURL     string `json:"html_url"`
	Title       string `json:"title"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
	PullRequest *struct {
		URL string `json:"url"`
	} `json:"pull_request"`
}

// ref builds the Ref string for an activity from a search item: the PR's
// source branch name (fetched separately, see headRef) followed by its URL.
// The branch name is where callers' Jira-key convention usually puts the key.
func (g *GitHubCollector) ref(item searchItem) string {
	if item.PullRequest == nil {
		return item.HTMLURL
	}
	if head := g.headRef(item.PullRequest.URL); head != "" {
		return head + " " + item.HTMLURL
	}
	return item.HTMLURL
}

func (g *GitHubCollector) searchPRs(query string) ([]model.Activity, error) {
	q := url.Values{}
	q.Set("q", query)
	q.Set("per_page", "100")
	q.Set("type", "pr")
	var result struct {
		Items []searchItem `json:"items"`
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
			Ref:    g.ref(pr),
		})
	}
	return acts, nil
}

// searchReviews finds PRs the user submitted a formal review on (approve,
// request changes, or review comment) for dayStr. Covers both open and
// merged PRs — GitHub does not restrict reviewed-by to any PR state.
func (g *GitHubCollector) searchReviews(dayStr string) ([]model.Activity, error) {
	q := url.Values{}
	q.Set("q", fmt.Sprintf("reviewed-by:%s updated:%s", g.username, dayStr))
	q.Set("per_page", "100")
	q.Set("type", "pr")
	var result struct {
		Items []searchItem `json:"items"`
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
			Ref:    g.ref(pr),
		})
	}
	return acts, nil
}

// searchComments finds PRs/issues the user left a plain comment on for dayStr.
// This catches conversation comments that aren't submitted as a formal review
// (approve/request-changes/comment), which searchReviews misses (#99).
func (g *GitHubCollector) searchComments(dayStr string) ([]model.Activity, error) {
	q := url.Values{}
	q.Set("q", fmt.Sprintf("commenter:%s updated:%s", g.username, dayStr))
	q.Set("per_page", "100")
	var result struct {
		Items []searchItem `json:"items"`
	}
	if err := g.get("/search/issues?"+q.Encode(), &result); err != nil {
		return nil, err
	}
	var acts []model.Activity
	for _, item := range result.Items {
		if item.PullRequest == nil {
			continue // plain issue comment, not a PR — not relevant here
		}
		t, _ := time.Parse(time.RFC3339, item.UpdatedAt)
		acts = append(acts, model.Activity{
			Date:   model.Day(t),
			Source: SourceGitHubComment,
			Text:   "Comment: " + item.Title,
			Ref:    g.ref(item),
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
	cmd, cancel := gitCommand(g.gitBin, "-C", repoPath, "remote", "get-url", "origin")
	defer cancel()
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

	cmd, cancel := gitCommand(g.gitBin, args...)
	defer cancel()
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
		branch := ""
		if len(parts) >= 4 && parts[3] != "" {
			branch = parts[3]
			ref = branch + commitTime
		} else {
			ref = strings.TrimSpace(commitTime)
		}
		acts = append(acts, model.Activity{
			Date:   day,
			Source: SourceLocalGit,
			Text:   subject,
			Ref:    strings.TrimSpace(ref),
			Branch: branch,
			Hash:   fullHash[:min(7, len(fullHash))],
		})
	}
	return acts, newHashes, nil
}

// collectReflog finds commits reachable via the reflog but NOT via --all
// branches (orphaned/rebased/detached-HEAD work). Limited to commits whose
// author date falls in [after, before] and matching the configured authors.
func (g *GitCollector) collectReflog(repoPath, after, before string, seenHash map[string]bool) ([]model.Activity, error) {
	// git log -g: walks the reflog instead of commit ancestry.
	// %D gives any ref decorations pointing at the commit (often empty for
	// orphaned work, in which case the branch is resolved separately below).
	args := []string{
		"-C", repoPath,
		"log", "-g",
		"--no-merges",
		"--format=%H%x1F%ae%x1F%aI%x1F%s%x1F%D",
		"--after=" + after,
		"--before=" + before,
	}
	for _, a := range g.authors {
		args = append(args, "--author="+a)
	}

	cmd, cancel := gitCommand(g.gitBin, args...)
	defer cancel()
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
		parts := strings.SplitN(line, "\x1f", 5)
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
		decorations := ""
		if len(parts) == 5 {
			decorations = parts[4]
		}
		branch := branchFromDecorations(decorations)
		if branch == "" {
			branch = g.branchContaining(repoPath, fullHash)
		}
		// Use the author date/time instead of the hash.
		commitTime := ""
		if t, err := time.Parse(time.RFC3339, parts[2]); err == nil {
			commitTime = t.Format("15:04")
		}
		ref := commitTime
		if branch != "" {
			if ref != "" {
				ref += " "
			}
			ref += branch
		} else {
			if ref != "" {
				ref += " "
			}
			ref += "(reflog)"
		}
		acts = append(acts, model.Activity{
			Date:   day,
			Source: SourceLocalGitReflog,
			Text:   subject,
			Ref:    ref,
			Branch: branch,
			Hash:   fullHash[:min(7, len(fullHash))],
		})
	}
	return acts, nil
}

// branchFromDecorations extracts a branch name from git's %D output, e.g.
// "HEAD -> feature/x, origin/feature/x" or "tag: v1, main". Tags are skipped.
func branchFromDecorations(d string) string {
	for _, part := range strings.Split(d, ",") {
		p := strings.TrimSpace(part)
		if p == "" || strings.HasPrefix(p, "tag: ") {
			continue
		}
		if i := strings.Index(p, "-> "); i >= 0 {
			p = strings.TrimSpace(p[i+3:])
		}
		if p == "HEAD" || p == "" {
			continue
		}
		return p
	}
	return ""
}

// branchContaining returns the first branch that contains the given commit, or
// "" when the commit is not reachable from any branch (orphaned/rebased away).
func (g *GitCollector) branchContaining(repoPath, hash string) string {
	cmd, cancel := gitCommand(g.gitBin, "-C", repoPath, "branch", "--all",
		"--contains", hash, "--format=%(refname:short)")
	defer cancel()
	hideCmd(cmd)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		b := strings.TrimSpace(line)
		if b != "" && b != "HEAD" {
			return b
		}
	}
	return ""
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
