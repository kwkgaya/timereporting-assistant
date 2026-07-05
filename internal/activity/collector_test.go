package activity

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// GitHub collector tests (against a local httptest server)
// ---------------------------------------------------------------------------

func makeGHServer() *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/search/issues", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("q")
		var items []map[string]any
		if q != "" {
			items = []map[string]any{
				{
					"html_url":   "https://github.com/org/repo/pull/42",
					"title":      "feat: EDB-100 add widget",
					"updated_at": "2026-06-03T10:00:00Z",
					"created_at": "2026-06-03T09:00:00Z",
					"head":       map[string]any{"ref": "EDB-100-add-widget"},
				},
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"total_count": len(items),
			"items":       items,
		})
	})
	return httptest.NewServer(mux)
}

func TestGitHubCollector_CollectForDay(t *testing.T) {
	ts := makeGHServer()
	defer ts.Close()

	c := NewGitHubCollector(ts.URL, "testuser", "")
	day := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	acts, err := c.CollectForDay(day)
	if err != nil {
		t.Fatalf("CollectForDay: %v", err)
	}
	if len(acts) == 0 {
		t.Fatal("expected at least one activity from mock GitHub")
	}
	for _, a := range acts {
		if a.Source != SourceGitHubPR && a.Source != SourceGitHubReview {
			t.Errorf("unexpected source %q", a.Source)
		}
	}
}

// ---------------------------------------------------------------------------
// Local git collector tests (against a real tmp git repo)
// ---------------------------------------------------------------------------

func initTempRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-b", "main")
	run("config", "user.email", "dev@example.com")
	run("config", "user.name", "Dev")
	f := filepath.Join(dir, "file.txt")
	_ = os.WriteFile(f, []byte("hello"), 0o644)
	run("add", ".")
	// Force commit date to a specific day.
	env := append(os.Environ(),
		"GIT_AUTHOR_DATE=2026-06-04T12:00:00+0000",
		"GIT_COMMITTER_DATE=2026-06-04T12:00:00+0000",
	)
	cmd := exec.Command("git", "-C", dir, "commit", "-m", "EDB-200 fix login bug")
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v\n%s", err, out)
	}
	return dir
}

func TestGitCollector_CollectForDay(t *testing.T) {
	repo := initTempRepo(t)

	c := NewGitCollector([]string{repo}, []string{"dev@example.com"})
	day := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)
	acts, err := c.CollectForDay(day)
	if err != nil {
		t.Fatalf("CollectForDay: %v", err)
	}
	if len(acts) == 0 {
		t.Fatal("expected at least one commit activity")
	}
	found := false
	for _, a := range acts {
		if a.Source == SourceLocalGit && a.Text == "EDB-200 fix login bug" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected commit subject in results; got %+v", acts)
	}
}

func TestGitCollector_WrongDayReturnsNothing(t *testing.T) {
	repo := initTempRepo(t)

	c := NewGitCollector([]string{repo}, []string{"dev@example.com"})
	// Ask for a different day.
	day := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	acts, err := c.CollectForDay(day)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 0 {
		t.Errorf("expected no activity for wrong day, got %+v", acts)
	}
}

func TestGitCollector_BadRepoSkipped(t *testing.T) {
	c := NewGitCollector([]string{"/nonexistent/repo"}, nil)
	day := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)
	acts, err := c.CollectForDay(day)
	// Should not fail — bad repos are skipped.
	if err != nil {
		t.Fatalf("expected no error for bad repo, got %v", err)
	}
	if len(acts) != 0 {
		t.Errorf("expected no activity from bad repo, got %+v", acts)
	}
}

func TestGitCollector_MultiCloneDedup(t *testing.T) {
	// Two dirs that are copies of the same repo (same commits, different paths).
	// The collector must only count each commit once.
	repo1 := initTempRepo(t)
	// Clone repo1 into a second dir.
	repo2 := t.TempDir()
	if out, err := exec.Command("git", "clone", repo1, repo2).CombinedOutput(); err != nil {
		t.Skipf("git clone failed: %v\n%s", err, out)
	}

	c := NewGitCollector([]string{repo1, repo2}, []string{"dev@example.com"})
	day := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)
	acts, err := c.CollectForDay(day)
	if err != nil {
		t.Fatalf("CollectForDay: %v", err)
	}
	// Should be exactly 1 — the commit exists in both clones but same origin.
	if len(acts) != 1 {
		t.Errorf("multi-clone dedup: got %d activities, want 1; %+v", len(acts), acts)
	}
}

func TestGitCollector_ReflogCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	dir := t.TempDir()
	run := func(env []string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	base := os.Environ()
	dateEnv := append(base,
		"GIT_AUTHOR_DATE=2026-06-04T12:00:00+0000",
		"GIT_COMMITTER_DATE=2026-06-04T12:00:00+0000",
		"GIT_AUTHOR_EMAIL=dev@example.com",
		"GIT_AUTHOR_NAME=Dev",
		"GIT_COMMITTER_EMAIL=dev@example.com",
		"GIT_COMMITTER_NAME=Dev",
	)

	run(base, "init", "-b", "main")
	run(base, "config", "user.email", "dev@example.com")
	run(base, "config", "user.name", "Dev")

	// Commit on main.
	_ = os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	run(base, "add", ".")
	run(dateEnv, "commit", "-m", "EDB-100 main commit")

	// Detached HEAD commit (will only be in reflog, not in --all after checkout).
	run(base, "checkout", "--detach")
	_ = os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644)
	run(base, "add", ".")
	run(dateEnv, "commit", "-m", "EDB-200 detached-head work")
	// Go back to main — the detached commit is now orphaned (not in --all).
	run(base, "checkout", "main")

	c := NewGitCollector([]string{dir}, []string{"dev@example.com"})
	day := time.Date(2026, 6, 4, 0, 0, 0, 0, time.UTC)
	acts, err := c.CollectForDay(day)
	if err != nil {
		t.Fatalf("CollectForDay: %v", err)
	}

	// Should find both commits: one from --all, one from reflog.
	texts := make(map[string]bool)
	for _, a := range acts {
		texts[a.Text] = true
	}
	if !texts["EDB-100 main commit"] {
		t.Error("missing main branch commit in results")
	}
	if !texts["EDB-200 detached-head work"] {
		t.Errorf("missing reflog-only commit; got acts: %+v", acts)
	}
}

